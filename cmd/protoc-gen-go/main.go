// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The protoc-gen-go binary is a protoc plugin to generate a Go protocol
// buffer package.
package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/golang/protobuf/proto"
	descpb "github.com/golang/protobuf/protoc-gen-go/descriptor"
	"google.golang.org/proto/protogen"
	"google.golang.org/proto/reflect/protoreflect"
)

const protoPackage = "github.com/golang/protobuf/proto"

func main() {
	protogen.Run(func(gen *protogen.Plugin) error {
		for _, f := range gen.Files {
			if !f.Generate {
				continue
			}
			genFile(gen, f)
		}
		return nil
	})
}

type File struct {
	*protogen.File
	locationMap   map[string][]*descpb.SourceCodeInfo_Location
	descriptorVar string // var containing the gzipped FileDescriptorProto
	init          []string
}

func genFile(gen *protogen.Plugin, file *protogen.File) {
	f := &File{
		File:        file,
		locationMap: make(map[string][]*descpb.SourceCodeInfo_Location),
	}
	for _, loc := range file.Proto.GetSourceCodeInfo().GetLocation() {
		key := pathKey(loc.Path)
		f.locationMap[key] = append(f.locationMap[key], loc)
	}

	// Determine the name of the var holding the file descriptor:
	//
	//     fileDescriptor_<hash of filename>
	filenameHash := sha256.Sum256([]byte(f.Desc.Path()))
	f.descriptorVar = fmt.Sprintf("fileDescriptor_%s", hex.EncodeToString(filenameHash[:8]))

	g := gen.NewGeneratedFile(f.GeneratedFilenamePrefix+".pb.go", f.GoImportPath)
	g.P("// Code generated by protoc-gen-go. DO NOT EDIT.")
	g.P("// source: ", f.Desc.Path())
	g.P()
	const filePackageField = 2 // FileDescriptorProto.package
	genComment(g, f, []int32{filePackageField})
	g.P()
	g.P("package ", f.GoPackageName)
	g.P()

	for _, enum := range f.Enums {
		genEnum(gen, g, f, enum)
	}
	for _, message := range f.Messages {
		genMessage(gen, g, f, message)
	}

	if len(f.init) != 0 {
		g.P("func init() {")
		for _, s := range f.init {
			g.P(s)
		}
		g.P("}")
		g.P()
	}

	genFileDescriptor(gen, g, f)
}

func genFileDescriptor(gen *protogen.Plugin, g *protogen.GeneratedFile, f *File) {
	// Trim the source_code_info from the descriptor.
	// Marshal and gzip it.
	descProto := proto.Clone(f.Proto).(*descpb.FileDescriptorProto)
	descProto.SourceCodeInfo = nil
	b, err := proto.Marshal(descProto)
	if err != nil {
		gen.Error(err)
		return
	}
	var buf bytes.Buffer
	w, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	w.Write(b)
	w.Close()
	b = buf.Bytes()

	g.P("func init() { proto.RegisterFile(", strconv.Quote(f.Desc.Path()), ", ", f.descriptorVar, ") }")
	g.P()
	g.P("var ", f.descriptorVar, " = []byte{")
	g.P("// ", len(b), " bytes of a gzipped FileDescriptorProto")
	for len(b) > 0 {
		n := 16
		if n > len(b) {
			n = len(b)
		}

		s := ""
		for _, c := range b[:n] {
			s += fmt.Sprintf("0x%02x,", c)
		}
		g.P(s)

		b = b[n:]
	}
	g.P("}")
	g.P()
}

func genEnum(gen *protogen.Plugin, g *protogen.GeneratedFile, f *File, enum *protogen.Enum) {
	genComment(g, f, enum.Path)
	// TODO: deprecation
	g.P("type ", enum.GoIdent, " int32")
	g.P("const (")
	for _, value := range enum.Values {
		genComment(g, f, value.Path)
		// TODO: deprecation
		g.P(value.GoIdent, " ", enum.GoIdent, " = ", value.Desc.Number())
	}
	g.P(")")
	g.P()
	nameMap := enum.GoIdent.GoName + "_name"
	g.P("var ", nameMap, " = map[int32]string{")
	generated := make(map[protoreflect.EnumNumber]bool)
	for _, value := range enum.Values {
		duplicate := ""
		if _, present := generated[value.Desc.Number()]; present {
			duplicate = "// Duplicate value: "
		}
		g.P(duplicate, value.Desc.Number(), ": ", strconv.Quote(string(value.Desc.Name())), ",")
		generated[value.Desc.Number()] = true
	}
	g.P("}")
	g.P()
	valueMap := enum.GoIdent.GoName + "_value"
	g.P("var ", valueMap, " = map[string]int32{")
	for _, value := range enum.Values {
		g.P(strconv.Quote(string(value.Desc.Name())), ": ", value.Desc.Number(), ",")
	}
	g.P("}")
	g.P()
	if enum.Desc.Syntax() != protoreflect.Proto3 {
		g.P("func (x ", enum.GoIdent, ") Enum() *", enum.GoIdent, " {")
		g.P("p := new(", enum.GoIdent, ")")
		g.P("*p = x")
		g.P("return p")
		g.P("}")
		g.P()
	}
	g.P("func (x ", enum.GoIdent, ") String() string {")
	g.P("return ", protogen.GoIdent{GoImportPath: protoPackage, GoName: "EnumName"}, "(", enum.GoIdent, "_name, int32(x))")
	g.P("}")
	g.P()

	if enum.Desc.Syntax() != protoreflect.Proto3 {
		g.P("func (x *", enum.GoIdent, ") UnmarshalJSON(data []byte) error {")
		g.P("value, err := ", protogen.GoIdent{GoImportPath: protoPackage, GoName: "UnmarshalJSONEnum"}, "(", enum.GoIdent, `_value, data, "`, enum.GoIdent, `")`)
		g.P("if err != nil {")
		g.P("return err")
		g.P("}")
		g.P("*x = ", enum.GoIdent, "(value)")
		g.P("return nil")
		g.P("}")
		g.P()
	}

	var indexes []string
	for i := 1; i < len(enum.Path); i += 2 {
		indexes = append(indexes, strconv.Itoa(int(enum.Path[i])))
	}
	g.P("func (", enum.GoIdent, ") EnumDescriptor() ([]byte, []int) {")
	g.P("return ", f.descriptorVar, ", []int{", strings.Join(indexes, ","), "}")
	g.P("}")
	g.P()

	genWellKnownType(g, enum.GoIdent, enum.Desc)

	// The name registered is, confusingly, <proto_package>.<go_ident>.
	// This probably should have been the full name of the proto enum
	// type instead, but changing it at this point would require thought.
	regName := string(f.Desc.Package()) + "." + enum.GoIdent.GoName
	f.init = append(f.init, fmt.Sprintf("%s(%q, %s, %s)",
		g.QualifiedGoIdent(protogen.GoIdent{
			GoImportPath: protoPackage,
			GoName:       "RegisterEnum",
		}),
		regName, nameMap, valueMap,
	))
}

func genMessage(gen *protogen.Plugin, g *protogen.GeneratedFile, f *File, message *protogen.Message) {
	for _, enum := range message.Enums {
		genEnum(gen, g, f, enum)
	}

	genComment(g, f, message.Path)
	g.P("type ", message.GoIdent, " struct {")
	g.P("}")
	g.P()

	for _, nested := range message.Messages {
		genMessage(gen, g, f, nested)
	}
}

func genComment(g *protogen.GeneratedFile, f *File, path []int32) {
	for _, loc := range f.locationMap[pathKey(path)] {
		if loc.LeadingComments == nil {
			continue
		}
		for _, line := range strings.Split(strings.TrimSuffix(loc.GetLeadingComments(), "\n"), "\n") {
			g.P("//", line)
		}
		return
	}
}

// pathKey converts a location path to a string suitable for use as a map key.
func pathKey(path []int32) string {
	var buf []byte
	for i, x := range path {
		if i != 0 {
			buf = append(buf, ',')
		}
		buf = strconv.AppendInt(buf, int64(x), 10)
	}
	return string(buf)
}

func genWellKnownType(g *protogen.GeneratedFile, ident protogen.GoIdent, desc protoreflect.Descriptor) {
	if wellKnownTypes[desc.FullName()] {
		g.P("func (", ident, `) XXX_WellKnownType() string { return "`, desc.Name(), `" }`)
		g.P()
	}
}

// Names of messages and enums for which we will generate XXX_WellKnownType methods.
var wellKnownTypes = map[protoreflect.FullName]bool{
	"google.protobuf.NullValue": true,
}
