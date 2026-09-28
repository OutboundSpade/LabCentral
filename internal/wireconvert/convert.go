package wireconvert

import (
	"fmt"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"reflect"
	"strings"
)

func field(v reflect.Value, j string) (reflect.Value, error) {
	t := v.Type()
	for n := 0; n < t.NumField(); n++ {
		name := strings.Split(t.Field(n).Tag.Get("json"), ",")[0]
		if name == "" {
			name = t.Field(n).Name
		}
		if name == j {
			return v.Field(n), nil
		}
	}
	return reflect.Value{}, fmt.Errorf("protocol field %s has no model counterpart", j)
}
func Encode(src any, dst proto.Message) error { return put(reflect.ValueOf(src), dst.ProtoReflect()) }
func put(v reflect.Value, m protoreflect.Message) error {
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	fs := m.Descriptor().Fields()
	for n := 0; n < fs.Len(); n++ {
		d := fs.Get(n)
		f, e := field(v, d.JSONName())
		if e != nil {
			return e
		}
		if d.IsMap() {
			mp := m.Mutable(d).Map()
			it := f.MapRange()
			for it.Next() {
				key := protoreflect.ValueOfString(it.Key().String()).MapKey()
				if d.MapValue().Kind() == protoreflect.MessageKind {
					mv := mp.NewValue()
					if e = put(it.Value(), mv.Message()); e != nil {
						return e
					}
					mp.Set(key, mv)
				} else {
					mp.Set(key, scalar(it.Value(), d.MapValue()))
				}
			}
		} else if d.IsList() {
			ls := m.Mutable(d).List()
			for i := 0; i < f.Len(); i++ {
				if d.Kind() == protoreflect.MessageKind {
					x := ls.NewElement()
					if e = put(f.Index(i), x.Message()); e != nil {
						return e
					}
					ls.Append(x)
				} else {
					ls.Append(scalar(f.Index(i), d))
				}
			}
		} else if d.Kind() == protoreflect.MessageKind {
			if e = put(f, m.Mutable(d).Message()); e != nil {
				return e
			}
		} else {
			m.Set(d, scalar(f, d))
		}
	}
	return nil
}
func scalar(f reflect.Value, d protoreflect.FieldDescriptor) protoreflect.Value {
	switch d.Kind() {
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(f.String())
	case protoreflect.BoolKind:
		return protoreflect.ValueOfBool(f.Bool())
	case protoreflect.Int32Kind:
		return protoreflect.ValueOfInt32(int32(f.Int()))
	case protoreflect.BytesKind:
		return protoreflect.ValueOfBytes(append([]byte(nil), f.Bytes()...))
	}
	panic("unsupported generated wire scalar")
}
func Decode(src proto.Message, dst any) error {
	return get(src.ProtoReflect(), reflect.ValueOf(dst).Elem())
}
func get(m protoreflect.Message, v reflect.Value) error {
	fs := m.Descriptor().Fields()
	for n := 0; n < fs.Len(); n++ {
		d := fs.Get(n)
		f, e := field(v, d.JSONName())
		if e != nil {
			return e
		}
		x := m.Get(d)
		if d.IsMap() {
			f.Set(reflect.MakeMap(f.Type()))
			var err error
			x.Map().Range(func(k protoreflect.MapKey, val protoreflect.Value) bool {
				entry := reflect.New(f.Type().Elem()).Elem()
				if d.MapValue().Kind() == protoreflect.MessageKind {
					err = get(val.Message(), entry)
				} else {
					assign(entry, val, d.MapValue())
				}
				f.SetMapIndex(reflect.ValueOf(k.String()), entry)
				return err == nil
			})
			if err != nil {
				return err
			}
		} else if d.IsList() {
			ls := x.List()
			f.Set(reflect.MakeSlice(f.Type(), ls.Len(), ls.Len()))
			for i := 0; i < ls.Len(); i++ {
				if d.Kind() == protoreflect.MessageKind {
					if e = get(ls.Get(i).Message(), f.Index(i)); e != nil {
						return e
					}
				} else {
					assign(f.Index(i), ls.Get(i), d)
				}
			}
		} else if d.Kind() == protoreflect.MessageKind {
			if e = get(x.Message(), f); e != nil {
				return e
			}
		} else {
			assign(f, x, d)
		}
	}
	return nil
}
func assign(f reflect.Value, v protoreflect.Value, d protoreflect.FieldDescriptor) {
	switch d.Kind() {
	case protoreflect.StringKind:
		f.SetString(v.String())
	case protoreflect.BoolKind:
		f.SetBool(v.Bool())
	case protoreflect.Int32Kind:
		f.SetInt(v.Int())
	case protoreflect.BytesKind:
		f.SetBytes(append([]byte(nil), v.Bytes()...))
	}
}
