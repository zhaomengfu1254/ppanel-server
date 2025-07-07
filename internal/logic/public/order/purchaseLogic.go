package order

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/perfect-panel/server/internal/model/coupon"
	"github.com/perfect-panel/server/internal/model/order"
	"github.com/perfect-panel/server/internal/model/user"
	"github.com/perfect-panel/server/internal/svc"
	"github.com/perfect-panel/server/internal/types"
	"github.com/perfect-panel/server/pkg/constant"
	"github.com/perfect-panel/server/pkg/logger"
	"github.com/perfect-panel/server/pkg/tool"
	"github.com/perfect-panel/server/pkg/uuidx"
	"github.com/perfect-panel/server/pkg/xerr"
	queue "github.com/perfect-panel/server/queue/types"
	"github.com/pkg/errors"
	"gorm.io/gorm"
	"time"
)

// PurchaseLogic 订单购买逻辑处理结构体
type PurchaseLogic struct {
	logger.Logger                     // 嵌入日志记录器
	ctx           context.Context     // 上下文
	svcCtx        *svc.ServiceContext // 服务上下文
}

// 系统常量定义
const (
	CloseOrderTimeMinutes = 15  // 未支付订单自动关闭时间（分钟）
	DefaultCommissionRate = 0.1 // 默认佣金比例（10%）

	// 订单状态常量
	OrderStatusPending  = 1 // 待支付
	OrderStatusPaid     = 2 // 已支付
	OrderStatusClosed   = 3 // 已关闭
	OrderStatusFailed   = 4 // 支付失败
	OrderStatusFinished = 5 // 已完成

	// 优惠券类型常量
	CouponTypePercentage  = 1 // 百分比折扣
	CouponTypeFixedAmount = 2 // 固定金额折扣

	// 订单类型常量
	OrderTypeSubscribe    = 1 // 订阅
	OrderTypeRenewal      = 2 // 续费
	OrderTypeResetTraffic = 3 // 重置流量
	OrderTypeRecharge     = 4 // 充值

	// 礼品金额日志类型
	GiftAmountTypeIncrease = 1 // 增加
	GiftAmountTypeReduce   = 2 // 减少
)

// NewPurchaseLogic 创建订阅购买逻辑处理器的实例
func NewPurchaseLogic(ctx context.Context, svcCtx *svc.ServiceContext) *PurchaseLogic {
	return &PurchaseLogic{
		Logger: logger.WithContext(ctx), // 初始化日志记录器
		ctx:    ctx,                     // 设置上下文
		svcCtx: svcCtx,                  // 设置服务上下文
	}
}

// Purchase 处理用户购买订阅的请求
// 参数：req - 包含购买信息的请求结构体
// 返回：resp - 包含订单号的响应结构体，err - 错误信息
func (l *PurchaseLogic) Purchase(req *types.PurchaseOrderRequest) (resp *types.PurchaseOrderResponse, err error) {
	// 从上下文中获取当前用户信息
	u, ok := l.ctx.Value(constant.CtxKeyUser).(*user.User)
	if !ok {
		logger.Error("上下文中未找到当前用户信息")
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.InvalidAccess), "无效访问")
	}

	// 查询用户的订阅信息
	userSub, err := l.svcCtx.UserModel.QueryUserSubscribe(l.ctx, u.Id)
	if err != nil {
		l.Errorw("[Purchase] 数据库查询错误", logger.Field("error", err.Error()), logger.Field("user_id", u.Id))
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.DatabaseQueryError), "查询用户订阅错误: %v", err.Error())
	}

	// 检查是否允许多个订阅（单订阅模式）
	if l.svcCtx.Config.Subscribe.SingleModel {
		if len(userSub) > 0 {
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.UserSubscribeExist), "用户已有订阅")
		}
	}

	// 查询订阅计划信息
	sub, err := l.svcCtx.SubscribeModel.FindOne(l.ctx, req.SubscribeId)
	if err != nil {
		l.Errorw("[Purchase] 数据库查询错误", logger.Field("error", err.Error()), logger.Field("subscribe_id", req.SubscribeId))
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.DatabaseQueryError), "查询订阅计划错误: %v", err.Error())
	}

	// 检查订阅计划是否可售
	if !*sub.Sell {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.ERROR), "该订阅计划不可购买")
	}

	// 检查用户是否超过订阅计划配额限制
	if sub.Quota > 0 {
		var count int64
		for _, v := range userSub {
			if v.SubscribeId == req.SubscribeId {
				count += 1
			}
		}
		if count >= sub.Quota {
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.SubscribeQuotaLimit), "超出配额限制")
		}
	}

	// 计算折扣
	var discount float64 = 1 // 默认折扣为1（无折扣）
	if sub.Discount != "" {
		var dis []types.SubscribeDiscount
		_ = json.Unmarshal([]byte(sub.Discount), &dis) // 解析折扣JSON数据
		discount = getDiscount(dis, req.Quantity)      // 根据购买数量获取对应折扣
	}

	// 计算价格和折扣金额
	price := sub.UnitPrice * req.Quantity      // 原始价格 = 单价 × 数量
	amount := int64(float64(price) * discount) // 折扣后金额 = 原始价格 × 折扣率
	discountAmount := price - amount           // 折扣金额 = 原始价格 - 折扣后金额

	var couponObj *coupon.Coupon
	var couponAmount int64 = 0 // 优惠券折扣金额

	// 处理优惠券折扣
	if req.Coupon != "" {
		// 根据优惠券码查找优惠券信息
		couponObj, err = l.svcCtx.CouponModel.FindOneByCode(l.ctx, req.Coupon)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errors.Wrapf(xerr.NewErrCode(xerr.CouponNotExist), "优惠券不存在")
			}
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.DatabaseQueryError), "查询优惠券错误: %v", err.Error())
		}

		// 检查优惠券是否启用（Enable 默认为true，所以只有当它明确设置为false时才返回错误）
		if couponObj.Enable != nil && *couponObj.Enable == false {
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.CouponNotEnable), "优惠券未启用")
		}

		// 检查优惠券有效期
		currentTime := time.Now().UnixMilli() // 获取毫秒级时间戳
		if couponObj.StartTime > 0 && currentTime < couponObj.StartTime {
			// 使用已定义的错误码
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.CouponNotApplicable), "优惠券未到使用时间")
		}
		if couponObj.ExpireTime > 0 && currentTime > couponObj.ExpireTime {
			// 使用已定义的错误码
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.CouponNotApplicable), "优惠券已过期")
		}

		// 检查优惠券是否有限制使用次数且已用完
		if couponObj.Count > 0 && couponObj.Count <= couponObj.UsedCount {
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.CouponInsufficientUsage), "优惠券已用完")
		}

		// 检查优惠券是否适用于当前订阅
		couponSub := tool.StringToInt64Slice(couponObj.Subscribe)
		if len(couponSub) > 0 && !tool.Contains(couponSub, req.SubscribeId) {
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.CouponNotApplicable), "优惠券不适用于此订阅")
		}

		// 检查用户是否已经超过优惠券使用限制
		var count int64
		err = l.svcCtx.DB.Transaction(func(tx *gorm.DB) error {
			return tx.Model(&order.Order{}).Where("user_id = ? and coupon = ?", u.Id, req.Coupon).Count(&count).Error
		})
		if err != nil {
			l.Errorw("[Purchase] 数据库查询错误", logger.Field("error", err.Error()), logger.Field("user_id", u.Id), logger.Field("coupon", req.Coupon))
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.DatabaseQueryError), "查询优惠券使用记录错误: %v", err.Error())
		}

		if couponObj.UserLimit > 0 && count >= couponObj.UserLimit {
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.CouponInsufficientUsage), "超出优惠券使用次数限制")
		}

		// 计算优惠券折扣金额
		couponAmount = calculateCoupon(amount, couponObj)
	}

	// 应用优惠券折扣
	amount -= couponAmount

	// 查询支付方式
	payment, err := l.svcCtx.PaymentModel.FindOne(l.ctx, req.Payment)
	if err != nil {
		l.Logger.Error("[Purchase] 数据库查询错误", logger.Field("error", err.Error()), logger.Field("payment", req.Payment))
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.DatabaseQueryError), "查询支付方式错误: %v", err.Error())
	}

	// 计算支付手续费
	var feeAmount int64 = 0
	if amount > 0 {
		feeAmount = calculateFee(amount, payment) // 计算手续费
	}

	// 判断用户是首次购买还是续费
	isNew, err := l.svcCtx.OrderModel.IsUserEligibleForNewOrder(l.ctx, u.Id)
	if err != nil {
		l.Errorw("[Purchase] 数据库查询错误", logger.Field("error", err.Error()), logger.Field("user_id", u.Id))
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.DatabaseQueryError), "查询用户订单历史错误: %v", err.Error())
	}

	// 计算佣金
	var commissionAmount int64 = 0
	var referrer *user.User

	// 如果用户有推荐人且是新订单，计算佣金
	if u.RefererId > 0 && isNew {
		// 查询推荐人信息
		referrer, err = l.svcCtx.UserModel.FindOne(l.ctx, u.RefererId)
		if err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				l.Errorw("[Purchase] 查询推荐人错误", logger.Field("error", err.Error()), logger.Field("referrer_id", u.RefererId))
				// 不中断流程，只记录错误
			}
		}

		if referrer != nil {
			// 从配置中获取推荐佣金比例
			// 注意：这里的比例是整数百分比（例如：10表示10%），需要除以100转换为小数
			commissionRate := float64(l.svcCtx.Config.Invite.ReferralPercentage) / 100.0

			// 基于实际支付金额计算佣金（amount是减去了所有折扣和优惠后的实际支付金额）
			commissionAmount = int64(float64(amount) * commissionRate)

			// 记录佣金计算过程
			l.Infow("[Purchase] 计算佣金",
				logger.Field("original_price", price),
				logger.Field("actual_amount", amount),
				logger.Field("commission_rate", commissionRate),
				logger.Field("commission_amount", commissionAmount),
			)
		}
	}

	// 创建订单对象
	// 移动佣金计算到事务内部，确保它是基于最终的实际支付金额计算的
	// 创建订单对象（不包含佣金字段）
	orderInfo := &order.Order{
		UserId:         u.Id,                   // 用户ID
		OrderNo:        tool.GenerateTradeNo(), // 生成订单号
		Type:           OrderTypeSubscribe,     // 订单类型：1表示订阅
		Quantity:       req.Quantity,           // 购买数量
		Price:          price,                  // 原始价格
		Amount:         amount + feeAmount,     // 应付金额（包含手续费）
		Discount:       discountAmount,         // 折扣金额
		Coupon:         req.Coupon,             // 优惠券码
		CouponDiscount: couponAmount,           // 优惠券折扣金额
		PaymentId:      payment.Id,             // 支付方式ID
		Method:         payment.Platform,       // 支付平台
		FeeAmount:      feeAmount,              // 手续费
		Status:         OrderStatusPending,     // 订单状态：1表示待支付
		IsNew:          isNew,                  // 是否新购买
		SubscribeId:    req.SubscribeId,        // 订阅计划ID
		Commission:     0,                      // 先设置为0，后面再计算
	}

	// 数据库事务处理
	err = l.svcCtx.DB.Transaction(func(tx *gorm.DB) error {
		// 计算礼品金额抵扣
		var deductionAmount int64 = 0

		// 如果用户有礼品金额，进行抵扣处理
		if u.GiftAmount > 0 {
			if u.GiftAmount >= orderInfo.Amount {
				// 礼品金额足够支付全部
				deductionAmount = orderInfo.Amount
				u.GiftAmount -= orderInfo.Amount
				orderInfo.Amount = 0 // 订单金额为0
			} else {
				// 礼品金额不足，部分抵扣
				deductionAmount = u.GiftAmount
				orderInfo.Amount -= u.GiftAmount
				u.GiftAmount = 0
			}

			// 设置订单礼品金额抵扣字段
			orderInfo.GiftAmount = deductionAmount

			// 更新用户信息
			if err := tx.Model(&user.User{}).Where("id = ?", u.Id).Update("gift_amount", u.GiftAmount).Error; err != nil {
				l.Errorw("[Purchase] 更新用户礼品金额错误", logger.Field("error", err.Error()), logger.Field("user_id", u.Id))
				return err
			}

			// 创建礼品金额使用记录
			giftAmountLog := user.GiftAmountLog{
				UserId:    u.Id,
				OrderNo:   orderInfo.OrderNo,
				Amount:    deductionAmount,
				Type:      GiftAmountTypeReduce, // 2表示抵扣
				Balance:   u.GiftAmount,
				Remark:    "购买订单抵扣",
				CreatedAt: time.Now(),
			}

			if err := tx.Model(&user.GiftAmountLog{}).Create(&giftAmountLog).Error; err != nil {
				l.Errorw("[Purchase] 创建礼品金额记录错误",
					logger.Field("error", err.Error()),
					logger.Field("log", giftAmountLog),
				)
				return err
			}
		}

		// 计算佣金 - 基于实际支付金额计算
		// 计算实际商品金额，不包含手续费，但减去了折扣和优惠券
		actualProductAmount := price - discountAmount - couponAmount

		// 如果用户有推荐人且是新订单，计算佣金
		if u.RefererId > 0 && isNew {
			// 查询推荐人信息
			referrer, err := l.svcCtx.UserModel.FindOne(l.ctx, u.RefererId)
			if err == nil && referrer != nil {
				// 从配置中获取推荐佣金比例
				commissionRate := float64(l.svcCtx.Config.Invite.ReferralPercentage) / 100.0

				// 计算礼品金额抵扣后的实际支付金额
				finalProductAmount := actualProductAmount - deductionAmount
				if finalProductAmount < 0 {
					finalProductAmount = 0 // 确保金额不为负
				}

				// 基于实际支付金额计算佣金
				commissionAmount := int64(float64(finalProductAmount) * commissionRate)
				orderInfo.Commission = commissionAmount

				// 记录佣金计算过程
				l.Infow("[Purchase] 计算佣金",
					logger.Field("original_price", price),
					logger.Field("discount_amount", discountAmount),
					logger.Field("coupon_amount", couponAmount),
					logger.Field("actual_product_amount", actualProductAmount),
					logger.Field("gift_deduction", deductionAmount),
					logger.Field("final_amount", finalProductAmount),
					logger.Field("commission_rate", commissionRate),
					logger.Field("commission_amount", commissionAmount),
				)
			}
		}

		// 插入订单记录
		if err := tx.WithContext(l.ctx).Model(&order.Order{}).Create(&orderInfo).Error; err != nil {
			return err
		}

		// 更新优惠券使用次数
		if req.Coupon != "" && couponObj != nil {
			couponObj.UsedCount += 1
			if err := tx.Model(&coupon.Coupon{}).Where("id = ?", couponObj.Id).Update("used_count", couponObj.UsedCount).Error; err != nil {
				l.Errorw("[Purchase] 更新优惠券使用次数错误",
					logger.Field("error", err.Error()),
					logger.Field("coupon", req.Coupon),
				)
				return err
			}
		}

		// 如果订单金额为0且有效，直接标记为已支付
		if orderInfo.Amount == 0 {
			orderInfo.Status = OrderStatusPaid // 2表示已支付
			orderInfo.UpdatedAt = time.Now()

			if err := tx.Model(&order.Order{}).Where("id = ?", orderInfo.Id).Updates(map[string]interface{}{
				"status":     orderInfo.Status,
				"updated_at": orderInfo.UpdatedAt,
			}).Error; err != nil {
				l.Errorw("[Purchase] 更新订单状态错误",
					logger.Field("error", err.Error()),
					logger.Field("order_id", orderInfo.Id),
				)
				return err
			}

			// 执行订单支付成功后的业务逻辑
			if err := l.processOrderPayment(tx, orderInfo, u); err != nil {
				l.Errorw("[Purchase] 处理订单支付业务错误",
					logger.Field("error", err.Error()),
					logger.Field("order_no", orderInfo.OrderNo),
				)
				return err
			}
		}

		return nil
	})

	if err != nil {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.DatabaseQueryError), "处理订单错误: %v", err.Error())
	}

	// 如果订单需要支付（金额>0），创建延迟关闭任务
	if orderInfo.Status == OrderStatusPending && orderInfo.Amount > 0 {
		if err := l.createCloseOrderTask(orderInfo.OrderNo); err != nil {
			// 只记录错误，不影响订单创建
			l.Errorw("[Purchase] 创建关闭订单任务失败", logger.Field("error", err.Error()), logger.Field("order_no", orderInfo.OrderNo))
		}
	}

	// 记录订单创建结果
	l.Infow("[Purchase] 订单创建成功",
		logger.Field("order_no", orderInfo.OrderNo),
		logger.Field("status", orderInfo.Status),
		logger.Field("amount", orderInfo.Amount),
		logger.Field("user_id", u.Id),
	)

	// 返回订单号
	return &types.PurchaseOrderResponse{
		OrderNo: orderInfo.OrderNo,
	}, nil
}

// processOrderPayment 处理订单支付成功后的业务逻辑
// 参数：
//   - tx: 数据库事务
//   - orderInfo: 订单信息
//   - u: 用户信息
//   - sub: 订阅计划信息
//
// 返回：错误信息
func (l *PurchaseLogic) processOrderPayment(tx *gorm.DB, orderInfo *order.Order, u *user.User) error {
	// 查找订阅计划信息
	sub, err := l.svcCtx.SubscribeModel.FindOne(l.ctx, orderInfo.SubscribeId)
	if err != nil {
		return errors.Wrapf(err, "查询订阅计划失败")
	}

	// 1. 创建用户订阅
	userSubscribe := &user.Subscribe{
		UserId:      orderInfo.UserId,
		OrderId:     orderInfo.Id,
		SubscribeId: orderInfo.SubscribeId,
		Token:       uuidx.SubscribeToken(orderInfo.OrderNo), // 生成订阅令牌
		UUID:        uuid.New().String(),                     // 生成UUID
		Status:      1,                                       // 1表示激活状态
		Traffic:     sub.Traffic,                             // 设置流量
		StartTime:   time.Now(),
		ExpireTime:  tool.AddTime(sub.UnitTime, orderInfo.Quantity, time.Now()), // 使用工具函数计算过期时间
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if err := tx.Model(&user.Subscribe{}).Create(userSubscribe).Error; err != nil {
		return errors.Wrapf(err, "创建用户订阅失败")
	}

	// 2. 如果有佣金，处理佣金记录
	if orderInfo.Commission > 0 && u.RefererId > 0 {
		// 创建佣金记录
		commissionLog := &user.CommissionLog{
			UserId:    u.RefererId,
			OrderNo:   orderInfo.OrderNo,
			Amount:    orderInfo.Commission,
			CreatedAt: time.Now(),
		}

		if err := tx.Model(&user.CommissionLog{}).Create(commissionLog).Error; err != nil {
			return errors.Wrapf(err, "创建佣金记录失败")
		}

		// 更新推荐人的佣金余额
		if err := tx.Model(&user.User{}).Where("id = ?", u.RefererId).
			UpdateColumn("commission", gorm.Expr("commission + ?", orderInfo.Commission)).Error; err != nil {
			return errors.Wrapf(err, "更新推荐人佣金余额失败")
		}
	}

	// 3. 更新订单状态为已完成
	if err := tx.Model(&order.Order{}).Where("id = ?", orderInfo.Id).
		Update("status", OrderStatusFinished).Error; err != nil { // 5表示已完成
		return errors.Wrapf(err, "更新订单状态失败")
	}

	return nil
}

// createCloseOrderTask 创建延迟关闭订单的任务
// 参数：orderNo - 订单号
// 返回：错误信息
func (l *PurchaseLogic) createCloseOrderTask(orderNo string) error {
	payload := queue.DeferCloseOrderPayload{
		OrderNo: orderNo,
	}

	// 序列化任务数据
	val, err := json.Marshal(payload)
	if err != nil {
		return errors.Wrapf(err, "序列化任务数据失败")
	}

	// 创建延迟任务，15分钟后执行
	task := asynq.NewTask(queue.DeferCloseOrder, val, asynq.MaxRetry(3))
	_, err = l.svcCtx.Queue.Enqueue(task, asynq.ProcessIn(CloseOrderTimeMinutes*time.Minute))
	if err != nil {
		return errors.Wrapf(err, "创建关闭订单任务失败")
	}

	return nil
}
