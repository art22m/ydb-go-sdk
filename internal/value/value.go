package value

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/ydb-platform/ydb-go-genproto/protos/Ydb"

	"github.com/ydb-platform/ydb-go-sdk/v3/internal/decimal"
	"github.com/ydb-platform/ydb-go-sdk/v3/internal/value/allocator"
	"github.com/ydb-platform/ydb-go-sdk/v3/internal/xerrors"
)

type Value interface {
	Type() Type
	String() string

	toString() string
	castTo(dst interface{}) error
	toYDB(a *allocator.Allocator) *Ydb.Value
}

func ToYDB(v Value, a *allocator.Allocator) *Ydb.TypedValue {
	tv := a.TypedValue()

	tv.Type = v.Type().toYDB(a)
	tv.Value = v.toYDB(a)

	return tv
}

// BigEndianUint128 builds a big-endian uint128 value.
func BigEndianUint128(hi, lo uint64) (v [16]byte) {
	binary.BigEndian.PutUint64(v[0:8], hi)
	binary.BigEndian.PutUint64(v[8:16], lo)
	return v
}

func FromYDB(t *Ydb.Type, v *Ydb.Value) Value {
	if vv, err := fromYDB(t, v); err != nil {
		panic(err)
	} else {
		return vv
	}
}

func nullValueFromYDB(x *Ydb.Value, t Type) (_ Value, ok bool) {
	for {
		switch xx := x.Value.(type) {
		case *Ydb.Value_NestedValue:
			x = xx.NestedValue
		case *Ydb.Value_NullFlagValue:
			switch tt := t.(type) {
			case *optionalType:
				return NullValue(tt.innerType), true
			case voidType:
				return VoidValue(), true
			default:
				return nil, false
			}
		default:
			return nil, false
		}
	}
}

func primitiveValueFromYDB(t PrimitiveType, v *Ydb.Value) (Value, error) {
	switch t {
	case TypeBool:
		return BoolValue(v.GetBoolValue()), nil

	case TypeInt8:
		return Int8Value(int8(v.GetInt32Value())), nil

	case TypeInt16:
		return Int16Value(int16(v.GetInt32Value())), nil

	case TypeInt32:
		return Int32Value(v.GetInt32Value()), nil

	case TypeInt64:
		return Int64Value(v.GetInt64Value()), nil

	case TypeUint8:
		return Uint8Value(uint8(v.GetUint32Value())), nil

	case TypeUint16:
		return Uint16Value(uint16(v.GetUint32Value())), nil

	case TypeUint32:
		return Uint32Value(v.GetUint32Value()), nil

	case TypeUint64:
		return Uint64Value(v.GetUint64Value()), nil

	case TypeDate:
		return DateValue(v.GetUint32Value()), nil

	case TypeDatetime:
		return DatetimeValue(v.GetUint32Value()), nil

	case TypeInterval:
		return IntervalValue(v.GetInt64Value()), nil

	case TypeTimestamp:
		return TimestampValue(v.GetUint64Value()), nil

	case TypeFloat:
		return FloatValue(v.GetFloatValue()), nil

	case TypeDouble:
		return DoubleValue(v.GetDoubleValue()), nil

	case TypeText:
		return TextValue(v.GetTextValue()), nil

	case TypeYSON:
		switch vv := v.GetValue().(type) {
		case *Ydb.Value_TextValue:
			return YSONValue([]byte(vv.TextValue)), nil
		case *Ydb.Value_BytesValue:
			return YSONValue(vv.BytesValue), nil
		default:
			return nil, xerrors.WithStackTrace(fmt.Errorf("uncovered YSON internal type: %T", vv))
		}

	case TypeJSON:
		return JSONValue(v.GetTextValue()), nil

	case TypeJSONDocument:
		return JSONDocumentValue(v.GetTextValue()), nil

	case TypeDyNumber:
		return DyNumberValue(v.GetTextValue()), nil

	case TypeTzDate:
		return TzDateValue(v.GetTextValue()), nil

	case TypeTzDatetime:
		return TzDatetimeValue(v.GetTextValue()), nil

	case TypeTzTimestamp:
		return TzTimestampValue(v.GetTextValue()), nil

	case TypeBytes:
		return BytesValue(v.GetBytesValue()), nil

	case TypeUUID:
		return UUIDValue(BigEndianUint128(v.High_128, v.GetLow_128())), nil

	default:
		return nil, xerrors.WithStackTrace(fmt.Errorf("uncovered primitive type: %T", t))
	}
}

func fromYDB(t *Ydb.Type, v *Ydb.Value) (Value, error) {
	tt := TypeFromYDB(t)

	if vv, ok := nullValueFromYDB(v, tt); ok {
		return vv, nil
	}

	switch ttt := tt.(type) {
	case PrimitiveType:
		return primitiveValueFromYDB(ttt, v)

	case voidType:
		return VoidValue(), nil

	case nullType:
		return NullValue(tt), nil

	case *DecimalType:
		return DecimalValue(BigEndianUint128(v.High_128, v.GetLow_128()), ttt.Precision, ttt.Scale), nil

	case *optionalType:
		t = t.Type.(*Ydb.Type_OptionalType).OptionalType.Item
		if nestedValue, ok := v.Value.(*Ydb.Value_NestedValue); ok {
			return OptionalValue(FromYDB(t, nestedValue.NestedValue)), nil
		}
		return OptionalValue(FromYDB(t, v)), nil

	case *listType:
		return ListValue(func() (vv []Value) {
			a := allocator.New()
			defer a.Free()
			for _, vvv := range v.Items {
				vv = append(vv, FromYDB(ttt.itemType.toYDB(a), vvv))
			}
			return vv
		}()...), nil

	case *TupleType:
		return TupleValue(func() (vv []Value) {
			a := allocator.New()
			defer a.Free()
			for i, vvv := range v.Items {
				vv = append(vv, FromYDB(ttt.items[i].toYDB(a), vvv))
			}
			return vv
		}()...), nil

	case *StructType:
		return StructValue(func() (vv []StructValueField) {
			a := allocator.New()
			defer a.Free()
			for i, vvv := range v.Items {
				vv = append(vv, StructValueField{
					Name: ttt.fields[i].Name,
					V:    FromYDB(ttt.fields[i].T.toYDB(a), vvv),
				})
			}
			return vv
		}()...), nil

	case *dictType:
		return DictValue(func() (vv []DictValueField) {
			a := allocator.New()
			defer a.Free()
			for _, vvv := range v.Pairs {
				vv = append(vv, DictValueField{
					K: FromYDB(ttt.keyType.toYDB(a), vvv.Key),
					V: FromYDB(ttt.valueType.toYDB(a), vvv.Payload),
				})
			}
			return vv
		}()...), nil

	case *variantType:
		a := allocator.New()
		defer a.Free()
		switch ttt.variantType {
		case variantTypeTuple:
			return VariantValueTuple(
				FromYDB(
					ttt.innerType.(*TupleType).toYDB(a),
					v.Value.(*Ydb.Value_NestedValue).NestedValue,
				),
				v.VariantIndex,
			), nil
		case variantTypeStruct:
			return VariantValueStruct(
				FromYDB(
					ttt.innerType.(*StructType).toYDB(a),
					v.Value.(*Ydb.Value_NestedValue).NestedValue,
				),
				v.VariantIndex,
			), nil
		default:
			return nil, fmt.Errorf("unknown variant type: %v", ttt.variantType)
		}

	default:
		return nil, xerrors.WithStackTrace(fmt.Errorf("uncovered type: %T", ttt))
	}
}

type boolValue bool

func (v boolValue) toString() string {
	return strconv.FormatBool(bool(v))
}

func (v boolValue) castTo(dst interface{}) error {
	switch vv := dst.(type) {
	case *bool:
		*vv = bool(v)
		return nil
	case *string:
		*vv = strconv.FormatBool(bool(v))
		return nil
	default:
		return xerrors.WithStackTrace(fmt.Errorf("cannot cast '%+v' to '%T' destination", v, vv))
	}
}

func (v boolValue) String() string {
	return fmt.Sprintf("%s(%q)", v.Type().String(), v.toString())
}

func (boolValue) Type() Type {
	return TypeBool
}

func (v boolValue) toYDB(a *allocator.Allocator) *Ydb.Value {
	vv := a.Bool()

	vv.BoolValue = bool(v)

	vvv := a.Value()
	vvv.Value = vv

	return vvv
}

func BoolValue(v bool) boolValue {
	return boolValue(v)
}

type dateValue uint32

func (v dateValue) toString() string {
	return DateToTime(uint32(v)).Format(LayoutDate)
}

func (v dateValue) castTo(dst interface{}) error {
	switch vv := dst.(type) {
	case *time.Time:
		*vv = DateToTime(uint32(v)).UTC()
		return nil
	case *uint64:
		*vv = uint64(v)
		return nil
	case *int64:
		*vv = int64(v)
		return nil
	case *int32:
		*vv = int32(v)
		return nil
	default:
		return xerrors.WithStackTrace(fmt.Errorf("cannot cast '%+v' to '%T' destination", v, vv))
	}
}

func (v dateValue) String() string {
	return fmt.Sprintf("%s(%q)", v.Type().String(), v.toString())
}

func (dateValue) Type() Type {
	return TypeDate
}

func (v dateValue) toYDB(a *allocator.Allocator) *Ydb.Value {
	vv := a.Uint32()

	vv.Uint32Value = uint32(v)

	vvv := a.Value()
	vvv.Value = vv

	return vvv
}

// DateValue returns ydb date value by given days since Epoch
func DateValue(v uint32) dateValue {
	return dateValue(v)
}

func DateValueFromTime(t time.Time) dateValue {
	return dateValue(uint64(t.Sub(epoch)/time.Second) / secondsPerDay)
}

type datetimeValue uint32

func (v datetimeValue) toString() string {
	return DatetimeToTime(uint32(v)).UTC().Format(LayoutDatetime)
}

func (v datetimeValue) castTo(dst interface{}) error {
	switch vv := dst.(type) {
	case *time.Time:
		*vv = DatetimeToTime(uint32(v)).UTC()
		return nil
	case *uint64:
		*vv = uint64(v)
		return nil
	case *int64:
		*vv = int64(v)
		return nil
	case *uint32:
		*vv = uint32(v)
		return nil
	default:
		return xerrors.WithStackTrace(fmt.Errorf("cannot cast '%+v' to '%T' destination", v, vv))
	}
}

func (v datetimeValue) String() string {
	return fmt.Sprintf("%s(%q)", v.Type().String(), v.toString())
}

func (datetimeValue) Type() Type {
	return TypeDatetime
}

func (v datetimeValue) toYDB(a *allocator.Allocator) *Ydb.Value {
	vv := a.Uint32()
	vv.Uint32Value = uint32(v)

	vvv := a.Value()
	vvv.Value = vv

	return vvv
}

// DatetimeValue makes ydb datetime value from seconds since Epoch
func DatetimeValue(v uint32) datetimeValue {
	return datetimeValue(v)
}

func DatetimeValueFromTime(t time.Time) datetimeValue {
	return datetimeValue(t.Unix())
}

type decimalValue struct {
	value     [16]byte
	innerType *DecimalType
}

func (v *decimalValue) toString() string {
	s := decimal.FromBytes(v.value[:], v.innerType.Precision, v.innerType.Scale).String()
	return s[:len(s)-int(v.innerType.Scale)] + "." + s[len(s)-int(v.innerType.Scale):]
}

func (v *decimalValue) castTo(dst interface{}) error {
	return xerrors.WithStackTrace(fmt.Errorf("cannot cast '%+v' to '%T' destination", v, dst))
}

func (v *decimalValue) String() string {
	return fmt.Sprintf("%s(%q,%d,%d)", v.innerType.Name(),
		v.toString(), v.innerType.Precision, v.innerType.Scale,
	)
}

func (v *decimalValue) Type() Type {
	return v.innerType
}

func (v *decimalValue) toYDB(a *allocator.Allocator) *Ydb.Value {
	var bytes [16]byte
	if v != nil {
		bytes = v.value
	}
	vv := a.Low128()
	vv.Low_128 = binary.BigEndian.Uint64(bytes[8:16])

	vvv := a.Value()
	vvv.High_128 = binary.BigEndian.Uint64(bytes[0:8])
	vvv.Value = vv

	return vvv
}

func DecimalValueFromBigInt(v *big.Int, precision, scale uint32) *decimalValue {
	b := decimal.BigIntToByte(v, precision, scale)
	return DecimalValue(b, precision, scale)
}

func DecimalValue(v [16]byte, precision uint32, scale uint32) *decimalValue {
	return &decimalValue{
		value: v,
		innerType: &DecimalType{
			Precision: precision,
			Scale:     scale,
		},
	}
}

type (
	DictValueField struct {
		K Value
		V Value
	}
	dictValue struct {
		t      Type
		values []DictValueField
	}
)

func (v *dictValue) toString() string {
	buffer := allocator.Buffers.Get()
	defer allocator.Buffers.Put(buffer)
	for i, value := range v.values {
		if i != 0 {
			buffer.WriteByte(',')
		}
		buffer.WriteString("AsTuple(")
		buffer.WriteString(value.K.String())
		buffer.WriteByte(',')
		buffer.WriteString(value.V.String())
		buffer.WriteByte(')')
	}
	return buffer.String()
}

func (v *dictValue) Values() map[Value]Value {
	values := make(map[Value]Value, len(v.values))
	for _, vv := range v.values {
		values[vv.K] = vv.V
	}
	return values
}

func (v *dictValue) castTo(dst interface{}) error {
	return xerrors.WithStackTrace(fmt.Errorf("cannot cast '%+v' to '%T' destination", v, dst))
}

func (v *dictValue) String() string {
	return fmt.Sprintf("AsDict(%s)", v.toString())
}

func (v *dictValue) Type() Type {
	return v.t
}

func (v *dictValue) toYDB(a *allocator.Allocator) *Ydb.Value {
	var values []DictValueField
	if v != nil {
		values = v.values
	}
	vvv := a.Value()

	for _, vv := range values {
		pair := a.Pair()

		pair.Key = vv.K.toYDB(a)
		pair.Payload = vv.V.toYDB(a)

		vvv.Pairs = append(vvv.Pairs, pair)
	}

	return vvv
}

func DictValue(values ...DictValueField) *dictValue {
	sort.Slice(values, func(i, j int) bool {
		return values[i].K.toString() < values[j].K.toString()
	})
	return &dictValue{
		t:      Dict(values[0].K.Type(), values[0].V.Type()),
		values: values,
	}
}

type doubleValue struct {
	value float64
}

func (v *doubleValue) toString() string {
	return fmt.Sprintf("%v", v.value)
}

func (v *doubleValue) castTo(dst interface{}) error {
	switch vv := dst.(type) {
	case *string:
		*vv = strconv.FormatFloat(v.value, 'f', -1, 64)
		return nil
	case *[]byte:
		*vv = []byte(strconv.FormatFloat(v.value, 'f', -1, 64))
		return nil
	case *float64:
		*vv = v.value
		return nil
	default:
		return xerrors.WithStackTrace(fmt.Errorf("cannot cast '%+v' to '%T' destination", v, vv))
	}
}

func (v *doubleValue) String() string {
	return fmt.Sprintf("%s(%q)", v.Type().String(), v.toString())
}

func (*doubleValue) Type() Type {
	return TypeDouble
}

func (v *doubleValue) toYDB(a *allocator.Allocator) *Ydb.Value {
	vv := a.Double()
	if v != nil {
		vv.DoubleValue = v.value
	}

	vvv := a.Value()
	vvv.Value = vv

	return vvv
}

func DoubleValue(v float64) *doubleValue {
	return &doubleValue{value: v}
}

type dyNumberValue string

func (v dyNumberValue) toString() string {
	return string(v)
}

func (v dyNumberValue) castTo(dst interface{}) error {
	switch vv := dst.(type) {
	case *string:
		*vv = string(v)
		return nil
	case *[]byte:
		*vv = []byte(v)
		return nil
	default:
		return xerrors.WithStackTrace(fmt.Errorf("cannot cast '%+v' to '%T' destination", v, vv))
	}
}

func (v dyNumberValue) String() string {
	return fmt.Sprintf("%s(%q)", v.Type().String(), v.toString())
}

func (dyNumberValue) Type() Type {
	return TypeDyNumber
}

func (v dyNumberValue) toYDB(a *allocator.Allocator) *Ydb.Value {
	vv := a.Text()
	vv.TextValue = string(v)

	vvv := a.Value()
	vvv.Value = vv

	return vvv
}

func DyNumberValue(v string) dyNumberValue {
	return dyNumberValue(v)
}

type floatValue struct {
	value float32
}

func (v *floatValue) toString() string {
	return fmt.Sprintf("%v", v.value)
}

func (v *floatValue) castTo(dst interface{}) error {
	switch vv := dst.(type) {
	case *string:
		*vv = strconv.FormatFloat(float64(v.value), 'f', -1, 32)
		return nil
	case *[]byte:
		*vv = []byte(strconv.FormatFloat(float64(v.value), 'f', -1, 32))
		return nil
	case *float64:
		*vv = float64(v.value)
		return nil
	case *float32:
		*vv = v.value
		return nil
	default:
		return xerrors.WithStackTrace(fmt.Errorf("cannot cast '%+v' to '%T' destination", v, vv))
	}
}

func (v *floatValue) String() string {
	return fmt.Sprintf("%s(%q)", v.Type().String(), v.toString())
}

func (*floatValue) Type() Type {
	return TypeFloat
}

func (v *floatValue) toYDB(a *allocator.Allocator) *Ydb.Value {
	vv := a.Float()
	if v != nil {
		vv.FloatValue = v.value
	}

	vvv := a.Value()
	vvv.Value = vv

	return vvv
}

func FloatValue(v float32) *floatValue {
	return &floatValue{value: v}
}

type int8Value int8

func (v int8Value) toString() string {
	return strconv.FormatInt(int64(v), 10)
}

func (v int8Value) castTo(dst interface{}) error {
	switch vv := dst.(type) {
	case *string:
		*vv = strconv.FormatInt(int64(v), 10)
		return nil
	case *[]byte:
		*vv = []byte(strconv.FormatInt(int64(v), 10))
		return nil
	case *int64:
		*vv = int64(v)
		return nil
	case *int32:
		*vv = int32(v)
		return nil
	case *int16:
		*vv = int16(v)
		return nil
	case *int8:
		*vv = int8(v)
		return nil
	case *float64:
		*vv = float64(v)
		return nil
	case *float32:
		*vv = float32(v)
		return nil
	default:
		return xerrors.WithStackTrace(fmt.Errorf("cannot cast '%+v' to '%T' destination", v, vv))
	}
}

func (v int8Value) String() string {
	return fmt.Sprintf("%s(%q)", v.Type().String(), v.toString())
}

func (int8Value) Type() Type {
	return TypeInt8
}

func (v int8Value) toYDB(a *allocator.Allocator) *Ydb.Value {
	vv := a.Int32()
	vv.Int32Value = int32(v)

	vvv := a.Value()
	vvv.Value = vv

	return vvv
}

func Int8Value(v int8) int8Value {
	return int8Value(v)
}

type int16Value int16

func (v int16Value) toString() string {
	return strconv.FormatInt(int64(v), 10)
}

func (v int16Value) castTo(dst interface{}) error {
	switch vv := dst.(type) {
	case *string:
		*vv = strconv.FormatInt(int64(v), 10)
		return nil
	case *[]byte:
		*vv = []byte(strconv.FormatInt(int64(v), 10))
		return nil
	case *int64:
		*vv = int64(v)
		return nil
	case *int32:
		*vv = int32(v)
		return nil
	case *int16:
		*vv = int16(v)
		return nil
	case *float64:
		*vv = float64(v)
		return nil
	case *float32:
		*vv = float32(v)
		return nil
	default:
		return xerrors.WithStackTrace(fmt.Errorf("cannot cast '%+v' to '%T' destination", v, vv))
	}
}

func (v int16Value) String() string {
	return fmt.Sprintf("%s(%q)", v.Type().String(), v.toString())
}

func (int16Value) Type() Type {
	return TypeInt16
}

func (v int16Value) toYDB(a *allocator.Allocator) *Ydb.Value {
	vv := a.Int32()
	vv.Int32Value = int32(v)

	vvv := a.Value()
	vvv.Value = vv

	return vvv
}

func Int16Value(v int16) int16Value {
	return int16Value(v)
}

type int32Value int32

func (v int32Value) toString() string {
	return strconv.FormatInt(int64(v), 10)
}

func (v int32Value) castTo(dst interface{}) error {
	switch vv := dst.(type) {
	case *string:
		*vv = strconv.FormatInt(int64(v), 10)
		return nil
	case *[]byte:
		*vv = []byte(strconv.FormatInt(int64(v), 10))
		return nil
	case *int64:
		*vv = int64(v)
		return nil
	case *int32:
		*vv = int32(v)
		return nil
	case *float64:
		*vv = float64(v)
		return nil
	case *float32:
		*vv = float32(v)
		return nil
	default:
		return xerrors.WithStackTrace(fmt.Errorf("cannot cast '%+v' to '%T' destination", v, vv))
	}
}

func (v int32Value) String() string {
	return fmt.Sprintf("%s(%q)", v.Type().String(), v.toString())
}

func (int32Value) Type() Type {
	return TypeInt32
}

func (v int32Value) toYDB(a *allocator.Allocator) *Ydb.Value {
	vv := a.Int32()
	vv.Int32Value = int32(v)

	vvv := a.Value()
	vvv.Value = vv

	return vvv
}

func Int32Value(v int32) int32Value {
	return int32Value(v)
}

type int64Value int64

func (v int64Value) toString() string {
	return strconv.FormatInt(int64(v), 10)
}

func (v int64Value) castTo(dst interface{}) error {
	switch vv := dst.(type) {
	case *string:
		*vv = strconv.FormatInt(int64(v), 10)
		return nil
	case *[]byte:
		*vv = []byte(strconv.FormatInt(int64(v), 10))
		return nil
	case *int64:
		*vv = int64(v)
		return nil
	case *float64:
		*vv = float64(v)
		return nil
	default:
		return xerrors.WithStackTrace(fmt.Errorf("cannot cast '%+v' to '%T' destination", v, vv))
	}
}

func (v int64Value) String() string {
	return fmt.Sprintf("%s(%q)", v.Type().String(), v.toString())
}

func (int64Value) Type() Type {
	return TypeInt64
}

func (v int64Value) toYDB(a *allocator.Allocator) *Ydb.Value {
	vv := a.Int64()
	vv.Int64Value = int64(v)

	vvv := a.Value()
	vvv.Value = vv

	return vvv
}

func Int64Value(v int64) int64Value {
	return int64Value(v)
}

type intervalValue int64

func (v intervalValue) toString() string {
	buffer := allocator.Buffers.Get()
	defer allocator.Buffers.Put(buffer)
	d := IntervalToDuration(int64(v))
	if d < 0 {
		buffer.WriteByte('-')
		d = -d
	}
	buffer.WriteByte('P')
	if days := d / time.Hour / 24; days > 0 {
		d -= days * time.Hour * 24
		buffer.WriteString(strconv.FormatInt(int64(days), 10))
		buffer.WriteByte('D')
	}
	if d > 0 {
		buffer.WriteByte('T')
	}
	if hours := d / time.Hour; hours > 0 {
		d -= hours * time.Hour
		buffer.WriteString(strconv.FormatInt(int64(hours), 10))
		buffer.WriteByte('H')
	}
	if minutes := d / time.Minute; minutes > 0 {
		d -= minutes * time.Minute
		buffer.WriteString(strconv.FormatInt(int64(minutes), 10))
		buffer.WriteByte('M')
	}
	if d > 0 {
		seconds := float64(d) / float64(time.Second)
		buffer.WriteString(fmt.Sprintf("%0.6f", seconds))
		buffer.WriteByte('S')
	}
	return buffer.String()
}

func (v intervalValue) castTo(dst interface{}) error {
	switch vv := dst.(type) {
	case *time.Duration:
		*vv = IntervalToDuration(int64(v))
		return nil
	case *int64:
		*vv = int64(v)
		return nil
	default:
		return xerrors.WithStackTrace(fmt.Errorf("cannot cast '%+v' to '%T' destination", v, vv))
	}
}

func (v intervalValue) String() string {
	return fmt.Sprintf("%s(%q)", v.Type().String(), v.toString())
}

func (intervalValue) Type() Type {
	return TypeInterval
}

func (v intervalValue) toYDB(a *allocator.Allocator) *Ydb.Value {
	vv := a.Int64()
	vv.Int64Value = int64(v)

	vvv := a.Value()
	vvv.Value = vv

	return vvv
}

// IntervalValue makes Value from given microseconds value
func IntervalValue(v int64) intervalValue {
	return intervalValue(v)
}

func IntervalValueFromDuration(v time.Duration) intervalValue {
	return intervalValue(durationToMicroseconds(v))
}

type jsonValue string

func (v jsonValue) toString() string {
	return string(v)
}

func (v jsonValue) castTo(dst interface{}) error {
	switch vv := dst.(type) {
	case *string:
		*vv = string(v)
		return nil
	case *[]byte:
		*vv = []byte(v)
		return nil
	default:
		return xerrors.WithStackTrace(fmt.Errorf("cannot cast '%+v' to '%T' destination", v, vv))
	}
}

func (v jsonValue) String() string {
	return fmt.Sprintf("%s(@@%s@@)", v.Type().String(), v.toString())
}

func (jsonValue) Type() Type {
	return TypeJSON
}

func (v jsonValue) toYDB(a *allocator.Allocator) *Ydb.Value {
	vv := a.Text()
	vv.TextValue = string(v)

	vvv := a.Value()
	vvv.Value = vv

	return vvv
}

func JSONValue(v string) jsonValue {
	return jsonValue(v)
}

type jsonDocumentValue string

func (v jsonDocumentValue) toString() string {
	return string(v)
}

func (v jsonDocumentValue) castTo(dst interface{}) error {
	switch vv := dst.(type) {
	case *string:
		*vv = string(v)
		return nil
	case *[]byte:
		*vv = []byte(v)
		return nil
	default:
		return xerrors.WithStackTrace(fmt.Errorf("cannot cast '%+v' to '%T' destination", v, vv))
	}
}

func (v jsonDocumentValue) String() string {
	return fmt.Sprintf("%s(@@%s@@)", v.Type().String(), v.toString())
}

func (jsonDocumentValue) Type() Type {
	return TypeJSONDocument
}

func (v jsonDocumentValue) toYDB(a *allocator.Allocator) *Ydb.Value {
	vv := a.Text()
	vv.TextValue = string(v)

	vvv := a.Value()
	vvv.Value = vv

	return vvv
}

func JSONDocumentValue(v string) jsonDocumentValue {
	return jsonDocumentValue(v)
}

type listValue struct {
	t     Type
	items []Value
}

func (v *listValue) toString() string {
	buffer := allocator.Buffers.Get()
	defer allocator.Buffers.Put(buffer)
	for i, item := range v.items {
		if i != 0 {
			buffer.WriteByte(',')
		}
		buffer.WriteString(item.toString())
	}
	return buffer.String()
}

func (v *listValue) castTo(dst interface{}) error {
	return xerrors.WithStackTrace(fmt.Errorf("cannot cast '%+v' to '%T' destination", v, dst))
}

func (v *listValue) String() string {
	return fmt.Sprintf("AsList(%s)", v.toString())
}

func (v *listValue) Type() Type {
	return v.t
}

func (v *listValue) toYDB(a *allocator.Allocator) *Ydb.Value {
	var items []Value
	if v != nil {
		items = v.items
	}
	vvv := a.Value()

	for _, vv := range items {
		vvv.Items = append(vvv.Items, vv.toYDB(a))
	}

	return vvv
}

func ListValue(items ...Value) *listValue {
	var t Type
	switch {
	case len(items) > 0:
		t = List(items[0].Type())
	default:
		t = EmptyList()
	}

	for _, v := range items {
		if !v.Type().equalsTo(v.Type()) {
			panic(fmt.Sprintf("different types of items: %v", items))
		}
	}
	return &listValue{
		t:     t,
		items: items,
	}
}

func NullValue(t Type) *optionalValue {
	return &optionalValue{
		innerType: Optional(t),
		value:     nil,
	}
}

type optionalValue struct {
	innerType Type
	value     Value
}

func (v *optionalValue) toString() string {
	if v.value == nil {
		return "NULL"
	} else {
		return v.value.toString()
	}
}

var errOptionalNilValue = errors.New("optional contains nil value")

func (v *optionalValue) castTo(dst interface{}) error {
	if v.value == nil {
		return xerrors.WithStackTrace(errOptionalNilValue)
	}
	return v.value.castTo(dst)
}

func (v *optionalValue) String() string {
	return fmt.Sprintf("CAST(%q AS %s)", v.toString(), v.Type().String())
}

func (v *optionalValue) Type() Type {
	return v.innerType
}

func (v *optionalValue) toYDB(a *allocator.Allocator) *Ydb.Value {
	vv := a.Value()
	if _, opt := v.value.(*optionalValue); opt {
		vvv := a.Nested()
		vvv.NestedValue = v.value.toYDB(a)
		vv.Value = vvv
	} else {
		if v.value != nil {
			vv.Value = v.value.toYDB(a).Value
		} else {
			vv.Value = a.NullFlag()
		}
	}
	return vv
}

func OptionalValue(v Value) *optionalValue {
	return &optionalValue{
		innerType: Optional(v.Type()),
		value:     v,
	}
}

type (
	StructValueField struct {
		Name string
		V    Value
	}
	structValue struct {
		t      Type
		fields []StructValueField
	}
)

func (v *structValue) toString() string {
	buffer := allocator.Buffers.Get()
	defer allocator.Buffers.Put(buffer)
	a := allocator.New()
	defer a.Free()
	for i, field := range v.fields {
		if i != 0 {
			buffer.WriteByte(',')
		}
		buffer.WriteString(field.V.String())
		buffer.WriteString(" AS `")
		buffer.WriteString(field.Name)
		buffer.WriteByte('`')
	}
	return buffer.String()
}

func (v *structValue) Fields() map[string]Value {
	fields := make(map[string]Value, len(v.fields))
	for _, f := range v.fields {
		fields[f.Name] = f.V
	}
	return fields
}

func (v *structValue) castTo(dst interface{}) error {
	return xerrors.WithStackTrace(fmt.Errorf("cannot cast '%+v' to '%T' destination", v, dst))
}

func (v *structValue) String() string {
	return fmt.Sprintf("AsStruct(%s)", v.toString())
}

func (v *structValue) Type() Type {
	return v.t
}

func (v structValue) toYDB(a *allocator.Allocator) *Ydb.Value {
	vvv := a.Value()

	for _, field := range v.fields {
		vvv.Items = append(vvv.Items, field.V.toYDB(a))
	}

	return vvv
}

func StructValue(fields ...StructValueField) *structValue {
	sort.Slice(fields, func(i, j int) bool {
		return fields[i].Name < fields[j].Name
	})
	structFields := make([]StructField, 0, len(fields))
	for _, field := range fields {
		structFields = append(structFields, StructField{field.Name, field.V.Type()})
	}
	return &structValue{
		t:      Struct(structFields...),
		fields: fields,
	}
}

type timestampValue uint64

func (v timestampValue) toString() string {
	return TimestampToTime(uint64(v)).UTC().Format(LayoutTimestamp)
}

func (v timestampValue) castTo(dst interface{}) error {
	switch vv := dst.(type) {
	case *time.Time:
		*vv = TimestampToTime(uint64(v)).UTC()
		return nil
	case *uint64:
		*vv = uint64(v)
		return nil
	default:
		return xerrors.WithStackTrace(fmt.Errorf("cannot cast '%+v' to '%T' destination", v, vv))
	}
}

func (v timestampValue) String() string {
	return fmt.Sprintf("%s(%q)", v.Type().String(), v.toString())
}

func (timestampValue) Type() Type {
	return TypeTimestamp
}

func (v timestampValue) toYDB(a *allocator.Allocator) *Ydb.Value {
	vv := a.Uint64()
	vv.Uint64Value = uint64(v)

	vvv := a.Value()
	vvv.Value = vv

	return vvv
}

// TimestampValue makes ydb timestamp value by given microseconds since Epoch
func TimestampValue(v uint64) timestampValue {
	return timestampValue(v)
}

func TimestampValueFromTime(t time.Time) timestampValue {
	return timestampValue(t.Sub(epoch) / time.Microsecond)
}

type tupleValue struct {
	t     Type
	items []Value
}

func (v *tupleValue) toString() string {
	buffer := allocator.Buffers.Get()
	defer allocator.Buffers.Put(buffer)
	for i, item := range v.items {
		if i != 0 {
			buffer.WriteByte(',')
		}
		buffer.WriteString(item.String())
	}
	return buffer.String()
}

func (v *tupleValue) Items() []Value {
	return v.items
}

func (v *tupleValue) castTo(dst interface{}) error {
	if len(v.items) == 1 {
		return v.items[0].castTo(dst)
	}
	return xerrors.WithStackTrace(fmt.Errorf("cannot cast '%+v' to '%T' destination", v, dst))
}

func (v *tupleValue) String() string {
	return fmt.Sprintf("AsTuple(%s)", v.toString())
}

func (v *tupleValue) Type() Type {
	return v.t
}

func (v *tupleValue) toYDB(a *allocator.Allocator) *Ydb.Value {
	var items []Value
	if v != nil {
		items = v.items
	}
	vvv := a.Value()

	for _, vv := range items {
		vvv.Items = append(vvv.Items, vv.toYDB(a))
	}

	return vvv
}

func TupleValue(values ...Value) *tupleValue {
	tupleItems := make([]Type, 0, len(values))
	for _, v := range values {
		tupleItems = append(tupleItems, v.Type())
	}
	return &tupleValue{
		t:     Tuple(tupleItems...),
		items: values,
	}
}

type tzDateValue string

func (v tzDateValue) toString() string {
	return string(v)
}

func (v tzDateValue) castTo(dst interface{}) error {
	switch vv := dst.(type) {
	case *string:
		*vv = string(v)
		return nil
	case *[]byte:
		*vv = []byte(v)
		return nil
	default:
		return xerrors.WithStackTrace(fmt.Errorf("cannot cast '%+v' to '%T' destination", v, vv))
	}
}

func (v tzDateValue) String() string {
	return fmt.Sprintf("%s(%q)", v.Type().String(), v.toString())
}

func (tzDateValue) Type() Type {
	return TypeTzDate
}

func (v tzDateValue) toYDB(a *allocator.Allocator) *Ydb.Value {
	vv := a.Text()
	vv.TextValue = string(v)

	vvv := a.Value()
	vvv.Value = vv

	return vvv
}

func TzDateValue(v string) tzDateValue {
	return tzDateValue(v)
}

func TzDateValueFromTime(t time.Time) tzDateValue {
	return tzDateValue(t.Format(LayoutDate))
}

type tzDatetimeValue string

func (v tzDatetimeValue) toString() string {
	return string(v)
}

func (v tzDatetimeValue) castTo(dst interface{}) error {
	switch vv := dst.(type) {
	case *string:
		*vv = string(v)
		return nil
	case *[]byte:
		*vv = []byte(v)
		return nil
	default:
		return xerrors.WithStackTrace(fmt.Errorf("cannot cast '%+v' to '%T' destination", v, vv))
	}
}

func (v tzDatetimeValue) String() string {
	return fmt.Sprintf("%s(%q)", v.Type().String(), v.toString())
}

func (tzDatetimeValue) Type() Type {
	return TypeTzDatetime
}

func (v tzDatetimeValue) toYDB(a *allocator.Allocator) *Ydb.Value {
	vv := a.Text()
	vv.TextValue = string(v)

	vvv := a.Value()
	vvv.Value = vv

	return vvv
}

func TzDatetimeValue(v string) tzDatetimeValue {
	return tzDatetimeValue(v)
}

func TzDatetimeValueFromTime(t time.Time) tzDatetimeValue {
	return tzDatetimeValue(t.Format(LayoutDatetime))
}

type tzTimestampValue string

func (v tzTimestampValue) toString() string {
	return string(v)
}

func (v tzTimestampValue) castTo(dst interface{}) error {
	switch vv := dst.(type) {
	case *string:
		*vv = string(v)
		return nil
	case *[]byte:
		*vv = []byte(v)
		return nil
	default:
		return xerrors.WithStackTrace(fmt.Errorf("cannot cast '%+v' to '%T' destination", v, vv))
	}
}

func (v tzTimestampValue) String() string {
	return fmt.Sprintf("%s(%q)", v.Type().String(), v.toString())
}

func (tzTimestampValue) Type() Type {
	return TypeTzTimestamp
}

func (v tzTimestampValue) toYDB(a *allocator.Allocator) *Ydb.Value {
	vv := a.Text()
	vv.TextValue = string(v)

	vvv := a.Value()
	vvv.Value = vv

	return vvv
}

func TzTimestampValue(v string) tzTimestampValue {
	return tzTimestampValue(v)
}

func TzTimestampValueFromTime(t time.Time) tzTimestampValue {
	return tzTimestampValue(t.Format(LayoutTimestamp))
}

type uint8Value uint8

func (v uint8Value) toString() string {
	return strconv.FormatUint(uint64(v), 10)
}

func (v uint8Value) castTo(dst interface{}) error {
	switch vv := dst.(type) {
	case *string:
		*vv = strconv.FormatInt(int64(v), 10)
		return nil
	case *[]byte:
		*vv = []byte(strconv.FormatInt(int64(v), 10))
		return nil
	case *uint64:
		*vv = uint64(v)
		return nil
	case *int64:
		*vv = int64(v)
		return nil
	case *uint32:
		*vv = uint32(v)
		return nil
	case *int32:
		*vv = int32(v)
		return nil
	case *uint16:
		*vv = uint16(v)
		return nil
	case *int16:
		*vv = int16(v)
		return nil
	case *uint8:
		*vv = uint8(v)
		return nil
	case *float64:
		*vv = float64(v)
		return nil
	case *float32:
		*vv = float32(v)
		return nil
	default:
		return xerrors.WithStackTrace(fmt.Errorf("cannot cast '%+v' to '%T' destination", v, vv))
	}
}

func (v uint8Value) String() string {
	return fmt.Sprintf("%s(%q)", v.Type().String(), v.toString())
}

func (uint8Value) Type() Type {
	return TypeUint8
}

func (v uint8Value) toYDB(a *allocator.Allocator) *Ydb.Value {
	vv := a.Uint32()
	vv.Uint32Value = uint32(v)

	vvv := a.Value()
	vvv.Value = vv

	return vvv
}

func Uint8Value(v uint8) uint8Value {
	return uint8Value(v)
}

type uint16Value uint16

func (v uint16Value) toString() string {
	return strconv.FormatUint(uint64(v), 10)
}

func (v uint16Value) castTo(dst interface{}) error {
	switch vv := dst.(type) {
	case *string:
		*vv = strconv.FormatInt(int64(v), 10)
		return nil
	case *[]byte:
		*vv = []byte(strconv.FormatInt(int64(v), 10))
		return nil
	case *uint64:
		*vv = uint64(v)
		return nil
	case *int64:
		*vv = int64(v)
		return nil
	case *uint32:
		*vv = uint32(v)
		return nil
	case *int32:
		*vv = int32(v)
		return nil
	case *uint16:
		*vv = uint16(v)
		return nil
	case *float32:
		*vv = float32(v)
		return nil
	case *float64:
		*vv = float64(v)
		return nil
	default:
		return xerrors.WithStackTrace(fmt.Errorf("cannot cast '%+v' to '%T' destination", v, vv))
	}
}

func (v uint16Value) String() string {
	return fmt.Sprintf("%s(%q)", v.Type().String(), v.toString())
}

func (uint16Value) Type() Type {
	return TypeUint16
}

func (v uint16Value) toYDB(a *allocator.Allocator) *Ydb.Value {
	vv := a.Uint32()
	vv.Uint32Value = uint32(v)

	vvv := a.Value()
	vvv.Value = vv

	return vvv
}

func Uint16Value(v uint16) uint16Value {
	return uint16Value(v)
}

type uint32Value uint32

func (v uint32Value) toString() string {
	return strconv.FormatUint(uint64(v), 10)
}

func (v uint32Value) castTo(dst interface{}) error {
	switch vv := dst.(type) {
	case *string:
		*vv = strconv.FormatInt(int64(v), 10)
		return nil
	case *[]byte:
		*vv = []byte(strconv.FormatInt(int64(v), 10))
		return nil
	case *uint64:
		*vv = uint64(v)
		return nil
	case *int64:
		*vv = int64(v)
		return nil
	case *uint32:
		*vv = uint32(v)
		return nil
	case *float64:
		*vv = float64(v)
		return nil
	default:
		return xerrors.WithStackTrace(fmt.Errorf("cannot cast '%+v' to '%T' destination", v, vv))
	}
}

func (v uint32Value) String() string {
	return fmt.Sprintf("%s(%q)", v.Type().String(), v.toString())
}

func (uint32Value) Type() Type {
	return TypeUint32
}

func (v uint32Value) toYDB(a *allocator.Allocator) *Ydb.Value {
	vv := a.Uint32()
	vv.Uint32Value = uint32(v)

	vvv := a.Value()
	vvv.Value = vv

	return vvv
}

func Uint32Value(v uint32) uint32Value {
	return uint32Value(v)
}

type uint64Value uint64

func (v uint64Value) toString() string {
	return strconv.FormatUint(uint64(v), 10)
}

func (v uint64Value) castTo(dst interface{}) error {
	switch vv := dst.(type) {
	case *string:
		*vv = strconv.FormatInt(int64(v), 10)
		return nil
	case *[]byte:
		*vv = []byte(strconv.FormatInt(int64(v), 10))
		return nil
	case *uint64:
		*vv = uint64(v)
		return nil
	default:
		return xerrors.WithStackTrace(fmt.Errorf("cannot cast '%+v' to '%T' destination", v, vv))
	}
}

func (v uint64Value) String() string {
	return fmt.Sprintf("%s(%q)", v.Type().String(), v.toString())
}

func (uint64Value) Type() Type {
	return TypeUint64
}

func (v uint64Value) toYDB(a *allocator.Allocator) *Ydb.Value {
	vv := a.Uint64()
	vv.Uint64Value = uint64(v)

	vvv := a.Value()
	vvv.Value = vv

	return vvv
}

func Uint64Value(v uint64) uint64Value {
	return uint64Value(v)
}

type textValue string

func (v textValue) toString() string {
	return string(v)
}

func (v textValue) castTo(dst interface{}) error {
	switch vv := dst.(type) {
	case *string:
		*vv = string(v)
		return nil
	case *[]byte:
		*vv = []byte(v)
		return nil
	default:
		return xerrors.WithStackTrace(fmt.Errorf("cannot cast '%+v' to '%T' destination", v, vv))
	}
}

func (v textValue) String() string {
	return fmt.Sprintf("%s(%q)", v.Type().String(), v.toString())
}

func (textValue) Type() Type {
	return TypeText
}

func (v textValue) toYDB(a *allocator.Allocator) *Ydb.Value {
	vv := a.Text()
	vv.TextValue = string(v)

	vvv := a.Value()
	vvv.Value = vv

	return vvv
}

func TextValue(v string) textValue {
	return textValue(v)
}

type uuidValue struct {
	value [16]byte
}

func (v *uuidValue) toString() string {
	return uuid.UUID(v.value).String()
}

func (v *uuidValue) castTo(dst interface{}) error {
	switch vv := dst.(type) {
	case *string:
		*vv = string(v.value[:])
		return nil
	case *[]byte:
		*vv = v.value[:]
		return nil
	case *[16]byte:
		*vv = v.value
		return nil
	default:
		return xerrors.WithStackTrace(fmt.Errorf("cannot cast '%+v' to '%T' destination", v, vv))
	}
}

func (v *uuidValue) String() string {
	return fmt.Sprintf("%s(%q)", v.Type().String(), v.toString())
}

func (*uuidValue) Type() Type {
	return TypeUUID
}

func (v *uuidValue) toYDB(a *allocator.Allocator) *Ydb.Value {
	var bytes [16]byte
	if v != nil {
		bytes = v.value
	}
	vv := a.Low128()
	vv.Low_128 = binary.BigEndian.Uint64(bytes[8:16])

	vvv := a.Value()
	vvv.High_128 = binary.BigEndian.Uint64(bytes[0:8])
	vvv.Value = vv

	return vvv
}

func UUIDValue(v [16]byte) *uuidValue {
	return &uuidValue{value: v}
}

type variantValue struct {
	innerType Type
	value     Value
	idx       uint32
}

func (v *variantValue) toString() string {
	buffer := allocator.Buffers.Get()
	defer allocator.Buffers.Put(buffer)
	buffer.WriteString(fmt.Sprintf("%q", v.value.toString()))
	buffer.WriteByte(',')
	buffer.WriteString(strconv.FormatUint(uint64(v.idx), 10))
	return buffer.String()
}

func (v *variantValue) castTo(dst interface{}) error {
	return v.value.castTo(dst)
}

func (v *variantValue) String() string {
	return fmt.Sprintf("AsVariant(%s)", v.toString())
}

func (v *variantValue) Type() Type {
	return v.innerType
}

func (v *variantValue) toYDB(a *allocator.Allocator) *Ydb.Value {
	vvv := a.Value()

	nested := a.Nested()
	nested.NestedValue = v.value.toYDB(a)

	vvv.Value = nested
	vvv.VariantIndex = v.idx

	return vvv
}

func VariantValue(v Value, idx uint32, t Type) *variantValue {
	return &variantValue{
		innerType: Variant(t),
		value:     v,
		idx:       idx,
	}
}

func VariantValueStruct(v Value, idx uint32) *variantValue {
	if _, ok := v.(*structValue); !ok {
		panic("value must be a struct type")
	}
	return &variantValue{
		innerType: &variantType{
			innerType:   v.Type(),
			variantType: variantTypeStruct,
		},
		value: v,
		idx:   idx,
	}
}

func VariantValueTuple(v Value, idx uint32) *variantValue {
	if _, ok := v.(*tupleValue); !ok {
		panic("value must be a tuple type")
	}
	return &variantValue{
		innerType: &variantType{
			innerType:   v.Type(),
			variantType: variantTypeTuple,
		},
		value: v,
		idx:   idx,
	}
}

type voidValue struct{}

func (v voidValue) toString() string {
	return "VOID"
}

func (v voidValue) castTo(dst interface{}) error {
	return xerrors.WithStackTrace(fmt.Errorf("cannot cast '%s' to '%T' destination", v.Type().String(), dst))
}

func (v voidValue) String() string {
	return v.toString()
}

var (
	_voidValueType = voidType{}
	_voidValue     = &Ydb.Value{
		Value: new(Ydb.Value_NullFlagValue),
	}
)

func (voidValue) Type() Type {
	return _voidValueType
}

func (voidValue) toYDB(*allocator.Allocator) *Ydb.Value {
	return _voidValue
}

func VoidValue() voidValue {
	return voidValue{}
}

type ysonValue []byte

func (v ysonValue) toString() string {
	return string(v)
}

func (v ysonValue) castTo(dst interface{}) error {
	switch vv := dst.(type) {
	case *string:
		*vv = string(v)
		return nil
	case *[]byte:
		*vv = v
		return nil
	default:
		return xerrors.WithStackTrace(fmt.Errorf("cannot cast '%+v' to '%T' destination", v, vv))
	}
}

func (v ysonValue) String() string {
	return fmt.Sprintf("%s(%q)", v.Type().String(), v.toString())
}

func (ysonValue) Type() Type {
	return TypeYSON
}

func (v ysonValue) toYDB(a *allocator.Allocator) *Ydb.Value {
	vv := a.Bytes()
	if v != nil {
		vv.BytesValue = v
	}

	vvv := a.Value()
	vvv.Value = vv

	return vvv
}

func YSONValue(v []byte) ysonValue {
	return v
}

func zeroPrimitiveValue(t PrimitiveType) Value {
	switch t {
	case TypeBool:
		return BoolValue(false)

	case TypeInt8:
		return Int8Value(0)

	case TypeUint8:
		return Uint8Value(0)

	case TypeInt16:
		return Int16Value(0)

	case TypeUint16:
		return Uint16Value(0)

	case TypeInt32:
		return Int32Value(0)

	case TypeUint32:
		return Uint32Value(0)

	case TypeInt64:
		return Int64Value(0)

	case TypeUint64:
		return Uint64Value(0)

	case TypeFloat:
		return FloatValue(0)

	case TypeDouble:
		return DoubleValue(0)

	case TypeDate:
		return DateValue(0)

	case TypeDatetime:
		return DatetimeValue(0)

	case TypeTimestamp:
		return TimestampValue(0)

	case TypeInterval:
		return IntervalValue(0)

	case TypeText:
		return TextValue("")

	case TypeYSON:
		return YSONValue([]byte(""))

	case TypeJSON:
		return JSONValue("")

	case TypeJSONDocument:
		return JSONDocumentValue("")

	case TypeDyNumber:
		return DyNumberValue("")

	case TypeTzDate:
		return TzDateValue("")

	case TypeTzDatetime:
		return TzDatetimeValue("")

	case TypeTzTimestamp:
		return TzTimestampValue("")

	case TypeBytes:
		return BytesValue([]byte{})

	case TypeUUID:
		return UUIDValue([16]byte{})

	default:
		panic(fmt.Sprintf("uncovered primitive type '%T'", t))
	}
}

func ZeroValue(t Type) Value {
	switch t := t.(type) {
	case PrimitiveType:
		return zeroPrimitiveValue(t)

	case *optionalType:
		return NullValue(t.innerType)

	case *voidType:
		return VoidValue()

	case *listType:
		return &listValue{
			t: t,
		}
	case *TupleType:
		v := &tupleValue{
			t:     t,
			items: make([]Value, len(t.items)),
		}
		for i, tt := range t.items {
			v.items[i] = ZeroValue(tt)
		}
		return v
	case *StructType:
		fields := make([]StructValueField, len(t.fields))
		for i, tt := range t.fields {
			fields[i] = StructValueField{
				Name: tt.Name,
				V:    ZeroValue(tt.T),
			}
		}
		return StructValue(fields...)
	case *dictType:
		return &dictValue{
			t: t,
		}
	case *DecimalType:
		return DecimalValue([16]byte{}, 22, 9)

	case *variantType:
		return VariantValue(ZeroValue(t.innerType), 0, t.innerType)

	default:
		panic(fmt.Sprintf("uncovered type '%T'", t))
	}
}

type bytesValue []byte

func (v bytesValue) toString() string {
	return string(v)
}

func (v bytesValue) castTo(dst interface{}) error {
	switch vv := dst.(type) {
	case *string:
		*vv = string(v)
		return nil
	case *[]byte:
		*vv = v
		return nil
	default:
		return xerrors.WithStackTrace(fmt.Errorf("cannot cast '%+v' to '%T' destination", v, vv))
	}
}

func (v bytesValue) String() string {
	return fmt.Sprintf("%s(%q)", v.Type().String(), v.toString())
}

func (bytesValue) Type() Type {
	return TypeBytes
}

func (v bytesValue) toYDB(a *allocator.Allocator) *Ydb.Value {
	vv := a.Bytes()

	vv.BytesValue = v

	vvv := a.Value()
	vvv.Value = vv

	return vvv
}

func BytesValue(v []byte) bytesValue {
	return v
}
