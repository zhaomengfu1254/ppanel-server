package order

import (
	"context"
	"encoding/json"

	"github.com/perfect-panel/server/internal/model/order"
	"github.com/perfect-panel/server/pkg/tool"

	"github.com/perfect-panel/server/pkg/constant"

	"github.com/perfect-panel/server/internal/model/user"
	"github.com/perfect-panel/server/internal/svc"
	"github.com/perfect-panel/server/internal/types"
	"github.com/perfect-panel/server/pkg/logger"
	"github.com/perfect-panel/server/pkg/xerr"
	"github.com/pkg/errors"
	"gorm.io/gorm"
)

/*

 */
// PreCreateOrderLogic 预创建订单的逻辑处理结构体
type PreCreateOrderLogic struct {
	logger.Logger                     // 嵌入日志记录器
	ctx           context.Context     // 上下文
	svcCtx        *svc.ServiceContext // 服务上下文
}

// NewPreCreateOrderLogic 创建预创建订单逻辑处理器的实例
func NewPreCreateOrderLogic(ctx context.Context, svcCtx *svc.ServiceContext) *PreCreateOrderLogic {
	return &PreCreateOrderLogic{
		Logger: logger.WithContext(ctx), // 初始化日志记录器
		ctx:    ctx,                     // 设置上下文
		svcCtx: svcCtx,                  // 设置服务上下文
	}
}

// PreCreateOrder 预创建订单的主要逻辑
func (l *PreCreateOrderLogic) PreCreateOrder(req *types.PurchaseOrderRequest) (resp *types.PreOrderResponse, err error) {
	// 从上下文中获取当前用户信息
	u, ok := l.ctx.Value(constant.CtxKeyUser).(*user.User)
	if !ok {
		logger.Error("current user is not found in context")                            // 记录错误日志
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.InvalidAccess), "Invalid Access") // 返回访问无效错误
	}
	// find subscribe plan 查找订阅计划
	sub, err := l.svcCtx.SubscribeModel.FindOne(l.ctx, req.SubscribeId)
	if err != nil {
		// 记录数据库查询错误
		l.Errorw("[PreCreateOrder] Database query error", logger.Field("error", err.Error()), logger.Field("subscribe_id", req.SubscribeId))
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.DatabaseQueryError), "find subscribe error: %v", err.Error())
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
	// 处理优惠券
	var couponAmount int64 = 0 // 优惠券折扣金额
	if req.Coupon != "" {
		// 根据优惠券码查找优惠券信息
		couponInfo, err := l.svcCtx.CouponModel.FindOneByCode(l.ctx, req.Coupon)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errors.Wrapf(xerr.NewErrCode(xerr.CouponNotExist), "coupon not found") // 优惠券不存在
			}
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.DatabaseQueryError), "find coupon error: %v", err.Error()) // 数据库查询错误
		}

		// 检查优惠券使用次数
		if couponInfo.Count > 0 && couponInfo.Count <= couponInfo.UsedCount {
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.CouponAlreadyUsed), "coupon used") // 优惠券已被使用
		}

		// 检查用户是否已经使用过此优惠券
		var count int64
		err = l.svcCtx.DB.Transaction(func(tx *gorm.DB) error {
			return tx.Model(&order.Order{}).Where("user_id = ? and coupon = ?", u.Id, req.Coupon).Count(&count).Error
		})
		if err != nil {
			l.Logger.Error("[PreCreateOrder] Database query error", logger.Field("error", err.Error()), logger.Field("user_id", u.Id), logger.Field("coupon", req.Coupon))
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.DatabaseQueryError), "find coupon error: %v", err.Error())
		}

		// 检查用户优惠券使用限制
		if couponInfo.UserLimit > 0 && count >= couponInfo.UserLimit {
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.CouponInsufficientUsage), "coupon limit exceeded") // 超出用户优惠券使用限制
		}

		// 检查优惠券是否适用于当前订阅
		couponSub := tool.StringToInt64Slice(couponInfo.Subscribe)
		if len(couponSub) > 0 && !tool.Contains(couponSub, req.SubscribeId) {
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.CouponNotApplicable), "coupon not match") // 优惠券不适用于此订阅
		}

		// 计算优惠券折扣金额
		couponAmount = calculateCoupon(amount, couponInfo)
	}
	// 应用优惠券折扣
	amount -= couponAmount

	// 处理礼品金额抵扣
	var deductionAmount int64 = 0
	if u.GiftAmount > 0 { // 如果用户有礼品金额
		if u.GiftAmount >= amount { // 如果礼品金额足够支付全部
			deductionAmount = amount
			amount = 0 // 订单金额为0
		} else { // 如果礼品金额不足
			deductionAmount = u.GiftAmount
			amount -= u.GiftAmount // 扣除礼品金额后的剩余订单金额
		}
	}
	// 计算支付手续费
	var feeAmount int64 = 0
	if req.Payment != 0 && amount > 0 { // 如果指定了支付方式且有需支付金额
		// 查找支付方式信息
		payment, err := l.svcCtx.PaymentModel.FindOne(l.ctx, req.Payment)
		if err != nil {
			l.Logger.Error("[PreCreateOrder] Database query error", logger.Field("error", err.Error()), logger.Field("payment", req.Payment))
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.DatabaseQueryError), "find payment method error: %v", err.Error())
		}

		// 计算手续费
		feeAmount = calculateFee(amount, payment)
		amount += feeAmount // 加上手续费
	}

	// 构建并返回预订单响应
	resp = &types.PreOrderResponse{
		Price:          price,           // 原始价格
		Amount:         amount,          // 最终支付金额
		Discount:       discountAmount,  // 折扣金额
		GiftAmount:     deductionAmount, // 礼品金额抵扣
		Coupon:         req.Coupon,      // 优惠券码
		CouponDiscount: couponAmount,    // 优惠券折扣金额
		FeeAmount:      feeAmount,       // 手续费
	}
	return
}
