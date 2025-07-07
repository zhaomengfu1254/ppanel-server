package order

import (
	"github.com/perfect-panel/server/internal/model/coupon"
)

// calculateCoupon 计算优惠券折扣金额
func calculateCoupon(amount int64, couponInfo *coupon.Coupon) int64 {
	var discount int64 = 0

	switch couponInfo.Type {
	case 1: // 百分比折扣（例如：打折券）
		// 计算百分比折扣金额：原价 × 折扣率
		discount = int64(float64(amount) * float64(couponInfo.Discount) / 100.0)

	case 2: // 固定金额折扣（例如：满减券）
		// 固定金额折扣不能超过原价
		discount = min(couponInfo.Discount, amount)
	}

	return discount
}
