package admin

import (
	"reflect"
	"sort"
	"strings"
)

// 请求价格使用指针区分未配置与显式 0；在转换为 float64 前保存这一事实。
func modelPriceRequestDisplayFields(req upsertModelPriceRequest) []string {
	value := reflect.ValueOf(req)
	typeInfo := value.Type()
	fields := []string{}
	for index := 0; index < value.NumField(); index++ {
		field := value.Field(index)
		name := strings.Split(typeInfo.Field(index).Tag.Get("json"), ",")[0]
		if field.Kind() != reflect.Ptr || field.IsNil() {
			continue
		}
		if strings.Contains(name, "cost_per_") || name == "per_request_price" || strings.HasPrefix(name, "video_price_per_second_") {
			fields = append(fields, name)
		}
	}
	sort.Strings(fields)
	return fields
}
