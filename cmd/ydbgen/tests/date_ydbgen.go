// Code generated by ydbgen; DO NOT EDIT.

package tests

import (
	"strconv"

	"github.com/YandexDatabase/ydb-go-sdk/v2"
	"github.com/YandexDatabase/ydb-go-sdk/v2/table"
)

var (
	_ = strconv.Itoa
	_ = ydb.StringValue
	_ = table.NewQueryParameters
)

func (t *Times) Scan(res *table.Result) (err error) {
	res.SeekItem("date")
	res.Unwrap()
	if !res.IsNull() {
		x0 := res.Date()
		err := (*ydb.Time)(&t.Date).FromDate(x0)
		if err != nil {
			panic("ydbgen: date type conversion failed: " + err.Error())
		}
	}

	return res.Err()
}

func (t *Times) QueryParameters() *table.QueryParameters {
	var v0 ydb.Value
	{
		var v1 ydb.Value
		var x0 uint32
		ok0 := !t.Date.IsZero()
		if ok0 {
			x0 = ydb.Time(t.Date).Date()
		}
		if ok0 {
			v1 = ydb.OptionalValue(ydb.DateValue(x0))
		} else {
			v1 = ydb.NullValue(ydb.TypeDate)
		}
		v0 = v1
	}
	return table.NewQueryParameters(
		table.ValueParam("$date", v0),
	)
}

func (t *Times) StructValue() ydb.Value {
	var v0 ydb.Value
	{
		var v1 ydb.Value
		{
			var v2 ydb.Value
			var x0 uint32
			ok0 := !t.Date.IsZero()
			if ok0 {
				x0 = ydb.Time(t.Date).Date()
			}
			if ok0 {
				v2 = ydb.OptionalValue(ydb.DateValue(x0))
			} else {
				v2 = ydb.NullValue(ydb.TypeDate)
			}
			v1 = v2
		}
		v0 = ydb.StructValue(
			ydb.StructFieldValue("date", v1),
		)
	}
	return v0
}

func (t *Times) StructType() ydb.Type {
	var t0 ydb.Type
	{
		fs0 := make([]ydb.StructOption, 1)
		var t1 ydb.Type
		{
			tp0 := ydb.TypeDate
			t1 = ydb.Optional(tp0)
		}
		fs0[0] = ydb.StructField("date", t1)
		t0 = ydb.Struct(fs0...)
	}
	return t0
}
