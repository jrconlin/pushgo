package simplepush

// AUTO GENERATED - DO NOT EDIT

import (
	C "github.com/glycerine/go-capnproto"
	"unsafe"
)

type Routable C.Struct

func NewRoutable(s *C.Segment) Routable      { return Routable(s.NewStruct(16, 1)) }
func NewRootRoutable(s *C.Segment) Routable  { return Routable(s.NewRootStruct(16, 1)) }
func ReadRootRoutable(s *C.Segment) Routable { return Routable(s.Root(0).ToStruct()) }
func (s Routable) ChannelID() string         { return C.Struct(s).GetObject(0).ToText() }
func (s Routable) SetChannelID(v string)     { C.Struct(s).SetObject(0, s.Segment.NewText(v)) }
func (s Routable) Version() int64            { return int64(C.Struct(s).Get64(0)) }
func (s Routable) SetVersion(v int64)        { C.Struct(s).Set64(0, uint64(v)) }
func (s Routable) Time() int64               { return int64(C.Struct(s).Get64(8)) }
func (s Routable) SetTime(v int64)           { C.Struct(s).Set64(8, uint64(v)) }

// capn.JSON_enabled == false so we stub MarshallJSON().
func (s Routable) MarshalJSON() (bs []byte, err error) { return }

type Routable_List C.PointerList

func NewRoutableList(s *C.Segment, sz int) Routable_List {
	return Routable_List(s.NewCompositeList(16, 1, sz))
}
func (s Routable_List) Len() int          { return C.PointerList(s).Len() }
func (s Routable_List) At(i int) Routable { return Routable(C.PointerList(s).At(i).ToStruct()) }
func (s Routable_List) ToArray() []Routable {
	return *(*[]Routable)(unsafe.Pointer(C.PointerList(s).ToArray()))
}
func (s Routable_List) Set(i int, item Routable) { C.PointerList(s).Set(i, C.Object(item)) }
