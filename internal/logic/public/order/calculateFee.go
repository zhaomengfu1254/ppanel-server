package order

import "github.com/perfect-panel/server/internal/model/payment"

// calculateFee 计算支付手续费
// 参数：amount - 订单金额，payment - 支付方式信息
// 返回：手续费金额
func calculateFee(amount int64, payment *payment.Payment) int64 {
	var fee float64

	// 根据支付方式的手续费规则计算
	switch payment.FeeMode {
	case 0: // 无手续费
		return 0
	case 1: // 百分比手续费
		fee = float64(amount) * (float64(payment.FeePercent) / float64(100))
	case 2: // 固定手续费
		if amount > 0 {
			fee = float64(payment.FeeAmount)
		}
	case 3: // 百分比 + 固定手续费
		fee = float64(amount)*(float64(payment.FeePercent)/float64(100)) + float64(payment.FeeAmount)
	}

	return int64(fee)
}
