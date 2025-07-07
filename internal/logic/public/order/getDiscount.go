package order

import "github.com/perfect-panel/server/internal/types"

//func getDiscount(discounts []types.SubscribeDiscount, inputMonths int64) float64 {
//	var finalDiscount int64 = 100
//
//	for _, discount := range discounts {
//		if inputMonths >= discount.Quantity && discount.Discount < finalDiscount {
//			finalDiscount = discount.Discount
//		}
//	}
//	return float64(finalDiscount) / float64(100)
//}

// getDiscount 根据购买数量获取对应的折扣率
// 参数：discounts - 折扣规则列表，inputMonths - 购买月数
// 返回：适用的折扣率
func getDiscount(discounts []types.SubscribeDiscount, inputMonths int64) float64 {
	var finalDiscount int64 = 100 // 默认折扣为100%（无折扣）

	// 遍历所有折扣规则，找到适用的最低折扣率
	for _, discount := range discounts {
		// 如果购买月数大于等于该规则的数量要求，且折扣率小于当前最低值
		if inputMonths >= discount.Quantity && discount.Discount < finalDiscount {
			finalDiscount = discount.Discount
		}
	}

	// 将百分比转为小数（例如：90% -> 0.9）
	return float64(finalDiscount) / float64(100)
}
