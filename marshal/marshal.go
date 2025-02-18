package marshal

import (
	"errors"
	"reflect"

	"github.com/AppliedIntuition/parquet-go/common"
	"github.com/AppliedIntuition/parquet-go/layout"
	"github.com/AppliedIntuition/parquet-go/parquet"
	"github.com/AppliedIntuition/parquet-go/schema"
	"github.com/AppliedIntuition/parquet-go/types"
)

type Node struct {
	Val     reflect.Value
	PathMap *schema.PathMapType
	RL      int32
	DL      int32
}

// Improve Performance///////////////////////////
// NodeBuf
type NodeBufType struct {
	Index int
	Buf   []*Node
}

func NewNodeBuf(ln int) *NodeBufType {
	nodeBuf := new(NodeBufType)
	nodeBuf.Index = 0
	nodeBuf.Buf = make([]*Node, ln)
	for i := 0; i < ln; i++ {
		nodeBuf.Buf[i] = new(Node)
	}
	return nodeBuf
}

func (nbt *NodeBufType) GetNode() *Node {
	if nbt.Index >= len(nbt.Buf) {
		nbt.Buf = append(nbt.Buf, new(Node))
	}
	nbt.Index++
	return nbt.Buf[nbt.Index-1]
}

func (nbt *NodeBufType) Reset() {
	nbt.Index = 0
}

// //////for improve performance///////////////////////////////////
type Marshaler interface {
	Marshal(node *Node, nodeBuf *NodeBufType, stack []*Node) (newStack []*Node)
}

type ParquetPtr struct{}

func (p *ParquetPtr) Marshal(node *Node, nodeBuf *NodeBufType, stack []*Node) []*Node {
	if node.Val.IsNil() {
		return stack
	}
	node.Val = node.Val.Elem()
	node.DL++
	stack = append(stack, node)
	return stack
}

type ParquetInterface struct{}

// For interface value, it doesn't introduce additional definition level since it's just proxy to the pointer of object.
// But the pointer of the object will introduce additional definition level
func (p *ParquetInterface) Marshal(node *Node, nodeBuf *NodeBufType, stack []*Node) []*Node {
	if node.Val.IsNil() {
		return stack
	}
	node.Val = node.Val.Elem()
	stack = append(stack, node)
	return stack
}

type ParquetStruct struct{}

func (p *ParquetStruct) Marshal(node *Node, nodeBuf *NodeBufType, stack []*Node) []*Node {
	var ok bool

	numField := node.Val.Type().NumField()
	for j := 0; j < numField; j++ {
		tf := node.Val.Type().Field(j)
		name := tf.Name
		if schema.IsInternalField(name) {
			continue
		}
		newNode := nodeBuf.GetNode()

		//some ignored item
		if newNode.PathMap, ok = node.PathMap.Children[name]; !ok {
			continue
		}

		newNode.Val = node.Val.Field(j)
		newNode.RL = node.RL
		newNode.DL = node.DL
		stack = append(stack, newNode)
	}
	return stack
}

type ParquetMapStruct struct {
	schemaHandler *schema.SchemaHandler
}

func (p *ParquetMapStruct) Marshal(node *Node, nodeBuf *NodeBufType, stack []*Node) []*Node {
	var ok bool

	keys := node.Val.MapKeys()
	if len(keys) <= 0 {
		return stack
	}

	missingKeys := make(map[string]bool)
	for k, typ := range node.PathMap.Children {
		if len(typ.Children) == 0 {
			missingKeys[k] = true
		}
	}
	for j := len(keys) - 1; j >= 0; j-- {
		key := keys[j]
		newNode := nodeBuf.GetNode()

		//some ignored item
		k := key.String()
		if newNode.PathMap, ok = node.PathMap.Children[k]; !ok {
			continue
		}
		missingKeys[k] = false
		v := node.Val.MapIndex(key)
		newNode.RL = node.RL
		newNode.DL = node.DL
		if v.Type().Kind() == reflect.Interface {
			newNode.Val = v.Elem()
			if newNode.Val.IsValid() {
				if *p.schemaHandler.SchemaElements[p.schemaHandler.MapIndex[newNode.PathMap.Path]].RepetitionType != parquet.FieldRepetitionType_REQUIRED {
					newNode.DL++
				}
			}
		} else {
			newNode.Val = v
		}
		stack = append(stack, newNode)
	}

	var null interface{}
	for k, isMissing := range missingKeys {
		if isMissing {
			newNode := nodeBuf.GetNode()
			newNode.PathMap = node.PathMap.Children[k]
			newNode.Val = reflect.ValueOf(null)
			newNode.RL = node.RL
			newNode.DL = node.DL
			stack = append(stack, newNode)
		}
	}
	return stack
}

type ParquetSlice struct {
	schemaHandler *schema.SchemaHandler
}

func (p *ParquetSlice) Marshal(node *Node, nodeBuf *NodeBufType, stack []*Node) []*Node {
	ln := node.Val.Len()
	pathMap := node.PathMap
	path := node.PathMap.Path
	if *p.schemaHandler.SchemaElements[p.schemaHandler.MapIndex[node.PathMap.Path]].RepetitionType != parquet.FieldRepetitionType_REPEATED {
		pathMap = pathMap.Children["List"].Children["Element"]
		path = path + common.PAR_GO_PATH_DELIMITER + "List" + common.PAR_GO_PATH_DELIMITER + "Element"
	}
	if ln <= 0 {
		return stack
	}

	rlNow, _ := p.schemaHandler.MaxRepetitionLevel(common.StrToPath(path))
	for j := ln - 1; j >= 0; j-- {
		newNode := nodeBuf.GetNode()
		newNode.PathMap = pathMap
		v := node.Val.Index(j)
		newNode.Val = v
		if j == 0 {
			newNode.RL = node.RL
		} else {
			newNode.RL = rlNow
		}
		newNode.DL = node.DL + 1
		stack = append(stack, newNode)
	}
	return stack
}

type ParquetMap struct {
	schemaHandler *schema.SchemaHandler
}

func (p *ParquetMap) Marshal(node *Node, nodeBuf *NodeBufType, stack []*Node) []*Node {
	path := node.PathMap.Path + common.PAR_GO_PATH_DELIMITER + "Key_value"
	keys := node.Val.MapKeys()
	if len(keys) <= 0 {
		return stack
	}

	rlNow, _ := p.schemaHandler.MaxRepetitionLevel(common.StrToPath(path))
	for j := len(keys) - 1; j >= 0; j-- {
		key := keys[j]
		value := node.Val.MapIndex(key)
		newNode := nodeBuf.GetNode()
		newNode.PathMap = node.PathMap.Children["Key_value"].Children["Key"]
		newNode.Val = key
		newNode.DL = node.DL + 1
		if j == 0 {
			newNode.RL = node.RL
		} else {
			newNode.RL = rlNow
		}
		stack = append(stack, newNode)

		newNode = nodeBuf.GetNode()
		newNode.PathMap = node.PathMap.Children["Key_value"].Children["Value"]
		newNode.Val = value
		newNode.DL = node.DL + 1
		if j == 0 {
			newNode.RL = node.RL
		} else {
			newNode.RL = rlNow
		}
		stack = append(stack, newNode)
	}
	return stack
}

func InitMarshal(srcInterface []interface{}, schemaHandler *schema.SchemaHandler) (*reflect.Value, *NodeBufType, map[string]*layout.Table) {
	src := reflect.ValueOf(srcInterface)
	res := setupTableMap(schemaHandler, len(srcInterface))
	nodeBuf := NewNodeBuf(1)

	for i := 0; i < len(schemaHandler.SchemaElements); i++ {
		schema := schemaHandler.SchemaElements[i]
		pathStr := schemaHandler.IndexMap[int32(i)]
		numChildren := schema.GetNumChildren()
		if numChildren == 0 {
			table := layout.NewEmptyTable()
			table.Path = common.StrToPath(pathStr)
			table.MaxDefinitionLevel, _ = schemaHandler.MaxDefinitionLevel(table.Path)
			table.MaxRepetitionLevel, _ = schemaHandler.MaxRepetitionLevel(table.Path)
			table.RepetitionType = schema.GetRepetitionType()
			table.Schema = schemaHandler.SchemaElements[schemaHandler.MapIndex[pathStr]]
			table.Info = schemaHandler.Infos[i]
			// Pre-size tables under the assumption that they'll be filled.
			table.Values = make([]interface{}, 0, len(srcInterface))
			table.DefinitionLevels = make([]int32, 0, len(srcInterface))
			table.RepetitionLevels = make([]int32, 0, len(srcInterface))
			res[pathStr] = table
		}
	}
	return &src, nodeBuf, res
}

func Marshal(srcInterface []interface{}, schemaHandler *schema.SchemaHandler) (tb *map[string]*layout.Table, err error) {
	return MarshalStruct(srcInterface, schemaHandler, false)
}

// Convert the objects to table map. srcInterface is a slice of objects
func MarshalStruct(srcInterface []interface{}, schemaHandler *schema.SchemaHandler, isProto bool) (tb *map[string]*layout.Table, err error) {
	defer func() {
		if r := recover(); r != nil {
			switch x := r.(type) {
			case string:
				err = errors.New(x)
			case error:
				err = x
			default:
				err = errors.New("unkown error")
			}
		}
	}()
	src, nodeBuf, res := InitMarshal(srcInterface, schemaHandler)
	pathMap := schemaHandler.PathMap
	stack := make([]*Node, 0, 100)
	for i := 0; i < len(srcInterface); i++ {
		stack = stack[:0]
		nodeBuf.Reset()

		node := nodeBuf.GetNode()
		node.Val = src.Index(i)
		// strip out the interface and pointer wrapper of the root value to make the definition level start as 0
		if node.Val.Kind() == reflect.Interface {
			node.Val = src.Index(i).Elem()
		}
		if schema.IsPointerGoTypeKind(node.Val.Kind()) {
			node.Val = node.Val.Elem()
		}
		node.PathMap = pathMap
		stack = append(stack, node)

		for len(stack) > 0 {
			ln := len(stack)
			node := stack[ln-1]
			stack = stack[:ln-1]

			tk := reflect.Interface
			if node.Val.IsValid() {
				tk = node.Val.Type().Kind()
			}
			var m Marshaler

			if tk == reflect.Ptr || tk == reflect.UnsafePointer || tk == reflect.Uintptr {
				m = &ParquetPtr{}
			} else if tk == reflect.Struct {
				if isProto && node.Val.Type() == schema.ProtoTimestampType {
					m = &ParquetTimestamp{}
				} else {
					m = &ParquetStruct{}
				}
			} else if (tk == reflect.Slice || tk == reflect.Array) && (node.Val.Type().Elem().Kind() != reflect.Uint8) {
				m = &ParquetSlice{schemaHandler: schemaHandler}
			} else if tk == reflect.Map {
				schemaIndex := schemaHandler.MapIndex[node.PathMap.Path]
				sele := schemaHandler.SchemaElements[schemaIndex]
				if !sele.IsSetConvertedType() {
					m = &ParquetMapStruct{schemaHandler: schemaHandler}
				} else {
					m = &ParquetMap{schemaHandler: schemaHandler}
				}
			} else if tk == reflect.Interface {
				m = &ParquetInterface{}
			} else {
				table := res[node.PathMap.Path]
				schemaIndex := schemaHandler.MapIndex[node.PathMap.Path]
				schemaDefinition := schemaHandler.SchemaElements[schemaIndex]
				var v interface{}
				if node.Val.IsValid() {
					v = node.Val.Interface()
				}
				// special handling for the enum
				if isProto && schemaDefinition.ConvertedType != nil && *schemaDefinition.ConvertedType == parquet.ConvertedType_ENUM {
					v = node.Val.MethodByName(schema.ProtoStringMethodName).Call(nil)[0].Interface().(string)
				}
				// special handling for the bytes
				if *schemaDefinition.Type == parquet.Type_BYTE_ARRAY && (node.Val.Type().Kind() == reflect.Slice || node.Val.Type().Kind() == reflect.Array) {
					v = string(node.Val.Bytes())

				}
				table.Values = append(table.Values, types.InterfaceToParquetType(v, schemaDefinition.Type))
				table.DefinitionLevels = append(table.DefinitionLevels, node.DL)
				table.RepetitionLevels = append(table.RepetitionLevels, node.RL)
				continue
			}

			oldLen := len(stack)
			stack = m.Marshal(node, nodeBuf, stack)
			if len(stack) == oldLen {
				path := node.PathMap.Path
				index := schemaHandler.MapIndex[path]
				numChildren := schemaHandler.SchemaElements[index].GetNumChildren()
				if numChildren > int32(0) {
					for key, table := range res {
						if common.IsChildPath(path, key) {
							table.Values = append(table.Values, nil)
							table.DefinitionLevels = append(table.DefinitionLevels, node.DL)
							table.RepetitionLevels = append(table.RepetitionLevels, node.RL)
						}
					}
				} else {
					table := res[path]
					table.Values = append(table.Values, nil)
					table.DefinitionLevels = append(table.DefinitionLevels, node.DL)
					table.RepetitionLevels = append(table.RepetitionLevels, node.RL)
				}
			}
		}
	}

	return &res, nil
}

func setupTableMap(schemaHandler *schema.SchemaHandler, numElements int) map[string]*layout.Table {
	tableMap := make(map[string]*layout.Table)
	for i := 0; i < len(schemaHandler.SchemaElements); i++ {
		schema := schemaHandler.SchemaElements[i]
		pathStr := schemaHandler.IndexMap[int32(i)]
		numChildren := schema.GetNumChildren()
		if numChildren == 0 {
			table := layout.NewEmptyTable()
			table.Path = common.StrToPath(pathStr)
			table.MaxDefinitionLevel, _ = schemaHandler.MaxDefinitionLevel(table.Path)
			table.MaxRepetitionLevel, _ = schemaHandler.MaxRepetitionLevel(table.Path)
			table.RepetitionType = schema.GetRepetitionType()
			table.Schema = schemaHandler.SchemaElements[schemaHandler.MapIndex[pathStr]]
			table.Info = schemaHandler.Infos[i]
			// Pre-size tables under the assumption that they'll be filled.
			table.Values = make([]interface{}, 0, numElements)
			table.DefinitionLevels = make([]int32, 0, numElements)
			table.RepetitionLevels = make([]int32, 0, numElements)
			tableMap[pathStr] = table
		}
	}
	return tableMap
}
