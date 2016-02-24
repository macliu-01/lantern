// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bind

import (
	"fmt"
	"go/token"
	"go/types"
	"strings"
)

type goGen struct {
	*printer
	fset *token.FileSet
	pkg  *types.Package
	err  ErrorList
}

func (g *goGen) errorf(format string, args ...interface{}) {
	g.err = append(g.err, fmt.Errorf(format, args...))
}

const goPreamble = `// Package go_%s is an autogenerated binder stub for package %s.
//   gobind -lang=go %s
//
// File is generated by gobind. Do not edit.
package go_%s

import (
	"golang.org/x/mobile/bind/seq"
	%q
)

`

func (g *goGen) genPreamble() {
	n := g.pkg.Name()
	g.Printf(goPreamble, n, n, g.pkg.Path(), n, g.pkg.Path())
}

func (g *goGen) genFuncBody(o *types.Func, selectorLHS string) {
	sig := o.Type().(*types.Signature)
	params := sig.Params()
	for i := 0; i < params.Len(); i++ {
		p := params.At(i)
		g.genRead("param_"+paramName(params, i), "in", p.Type())
	}

	res := sig.Results()
	if res.Len() > 2 || res.Len() == 2 && !isErrorType(res.At(1).Type()) {
		g.errorf("functions and methods must return either zero or one values, and optionally an error")
		return
	}
	returnsValue := false
	returnsError := false
	if res.Len() == 1 {
		if isErrorType(res.At(0).Type()) {
			returnsError = true
			g.Printf("err := ")
		} else {
			returnsValue = true
			g.Printf("res := ")
		}
	} else if res.Len() == 2 {
		returnsValue = true
		returnsError = true
		g.Printf("res, err := ")
	}

	g.Printf("%s.%s(", selectorLHS, o.Name())
	for i := 0; i < params.Len(); i++ {
		if i > 0 {
			g.Printf(", ")
		}
		g.Printf("param_%s", paramName(params, i))
	}
	g.Printf(")\n")

	if returnsValue {
		g.genWrite("res", "out", res.At(0).Type())
	}
	if returnsError {
		g.genWrite("err", "out", res.At(res.Len()-1).Type())
	}
}

func (g *goGen) genWrite(valName, seqName string, T types.Type) {
	if isErrorType(T) {
		g.Printf("if %s == nil {\n", valName)
		g.Printf("    %s.WriteString(\"\");\n", seqName)
		g.Printf("} else {\n")
		g.Printf("    %s.WriteString(%s.Error());\n", seqName, valName)
		g.Printf("}\n")
		return
	}
	switch T := T.(type) {
	case *types.Pointer:
		// TODO(crawshaw): test *int
		// TODO(crawshaw): test **Generator
		switch T := T.Elem().(type) {
		case *types.Named:
			obj := T.Obj()
			if obj.Pkg() != g.pkg {
				g.errorf("type %s not defined in %s", T, g.pkg)
				return
			}
			g.Printf("%s.WriteGoRef(%s)\n", seqName, valName)
		default:
			g.errorf("unsupported type %s", T)
		}
	case *types.Named:
		switch u := T.Underlying().(type) {
		case *types.Interface, *types.Pointer:
			g.Printf("%s.WriteGoRef(%s)\n", seqName, valName)
		default:
			g.errorf("unsupported, direct named type %s: %s", T, u)
		}
	default:
		g.Printf("%s.Write%s(%s);\n", seqName, seqType(T), valName)
	}
}

func (g *goGen) genFunc(o *types.Func) {
	g.Printf("func proxy_%s(out, in *seq.Buffer) {\n", o.Name())
	g.Indent()
	g.genFuncBody(o, g.pkg.Name())
	g.Outdent()
	g.Printf("}\n\n")
}

func (g *goGen) genStruct(obj *types.TypeName, T *types.Struct) {
	fields := exportedFields(T)
	methods := exportedMethodSet(types.NewPointer(obj.Type()))

	g.Printf("const (\n")
	g.Indent()
	g.Printf("proxy%s_Descriptor = \"go.%s.%s\"\n", obj.Name(), g.pkg.Name(), obj.Name())
	for i, f := range fields {
		g.Printf("proxy%s_%s_Get_Code = 0x%x0f\n", obj.Name(), f.Name(), i)
		g.Printf("proxy%s_%s_Set_Code = 0x%x1f\n", obj.Name(), f.Name(), i)
	}
	for i, m := range methods {
		g.Printf("proxy%s_%s_Code = 0x%x0c\n", obj.Name(), m.Name(), i)
	}
	g.Outdent()
	g.Printf(")\n\n")

	g.Printf("type proxy%s seq.Ref\n\n", obj.Name())

	for _, f := range fields {
		g.Printf("func proxy%s_%s_Set(out, in *seq.Buffer) {\n", obj.Name(), f.Name())
		g.Indent()
		g.Printf("ref := in.ReadRef()\n")
		g.genRead("v", "in", f.Type())
		g.Printf("ref.Get().(*%s.%s).%s = v\n", g.pkg.Name(), obj.Name(), f.Name())
		g.Outdent()
		g.Printf("}\n\n")

		g.Printf("func proxy%s_%s_Get(out, in *seq.Buffer) {\n", obj.Name(), f.Name())
		g.Indent()
		g.Printf("ref := in.ReadRef()\n")
		g.Printf("v := ref.Get().(*%s.%s).%s\n", g.pkg.Name(), obj.Name(), f.Name())
		g.genWrite("v", "out", f.Type())
		g.Outdent()
		g.Printf("}\n\n")
	}

	for _, m := range methods {
		g.Printf("func proxy%s_%s(out, in *seq.Buffer) {\n", obj.Name(), m.Name())
		g.Indent()
		g.Printf("ref := in.ReadRef()\n")
		g.Printf("v := ref.Get().(*%s.%s)\n", g.pkg.Name(), obj.Name())
		g.genFuncBody(m, "v")
		g.Outdent()
		g.Printf("}\n\n")
	}

	g.Printf("func init() {\n")
	g.Indent()
	for _, f := range fields {
		n := f.Name()
		g.Printf("seq.Register(proxy%s_Descriptor, proxy%s_%s_Set_Code, proxy%s_%s_Set)\n", obj.Name(), obj.Name(), n, obj.Name(), n)
		g.Printf("seq.Register(proxy%s_Descriptor, proxy%s_%s_Get_Code, proxy%s_%s_Get)\n", obj.Name(), obj.Name(), n, obj.Name(), n)
	}
	for _, m := range methods {
		n := m.Name()
		g.Printf("seq.Register(proxy%s_Descriptor, proxy%s_%s_Code, proxy%s_%s)\n", obj.Name(), obj.Name(), n, obj.Name(), n)
	}
	g.Outdent()
	g.Printf("}\n\n")
}

func (g *goGen) genVar(o *types.Var) {
	// TODO(hyangah): non-struct pointer types (*int), struct type.

	v := fmt.Sprintf("%s.%s", g.pkg.Name(), o.Name())

	// var I int
	//
	// func var_setI(out, in *seq.Buffer)
	g.Printf("func var_set%s(out, in *seq.Buffer) {\n", o.Name())
	g.Indent()
	g.genRead("v", "in", o.Type())
	g.Printf("%s = v\n", v)
	g.Outdent()
	g.Printf("}\n")

	// func var_getI(out, in *seq.Buffer)
	g.Printf("func var_get%s(out, in *seq.Buffer) {\n", o.Name())
	g.Indent()
	g.genWrite(v, "out", o.Type())
	g.Outdent()
	g.Printf("}\n")
}

func (g *goGen) genInterface(obj *types.TypeName) {
	iface := obj.Type().(*types.Named).Underlying().(*types.Interface)
	ifaceDesc := fmt.Sprintf("go.%s.%s", g.pkg.Name(), obj.Name())

	summary := makeIfaceSummary(iface)

	// Descriptor and code for interface methods.
	g.Printf("const (\n")
	g.Indent()
	g.Printf("proxy%s_Descriptor = %q\n", obj.Name(), ifaceDesc)
	for i, m := range summary.callable {
		g.Printf("proxy%s_%s_Code = 0x%x0a\n", obj.Name(), m.Name(), i+1)
	}
	g.Outdent()
	g.Printf(")\n\n")

	// Define the entry points.
	for _, m := range summary.callable {
		g.Printf("func proxy%s_%s(out, in *seq.Buffer) {\n", obj.Name(), m.Name())
		g.Indent()
		g.Printf("ref := in.ReadRef()\n")
		g.Printf("v := ref.Get().(%s.%s)\n", g.pkg.Name(), obj.Name())
		g.genFuncBody(m, "v")
		g.Outdent()
		g.Printf("}\n\n")
	}

	// Register the method entry points.
	if len(summary.callable) > 0 {
		g.Printf("func init() {\n")
		g.Indent()
		for _, m := range summary.callable {
			g.Printf("seq.Register(proxy%s_Descriptor, proxy%s_%s_Code, proxy%s_%s)\n",
				obj.Name(), obj.Name(), m.Name(), obj.Name(), m.Name())
		}
		g.Outdent()
		g.Printf("}\n\n")
	}

	// Define a proxy interface.
	if !summary.implementable {
		// The interface defines an unexported method or a method that
		// uses an unexported type. We cannot generate a proxy object
		// for such a type.
		return
	}
	g.Printf("type proxy%s seq.Ref\n\n", obj.Name())

	for i := 0; i < iface.NumMethods(); i++ {
		m := iface.Method(i)
		sig := m.Type().(*types.Signature)
		params := sig.Params()
		res := sig.Results()

		if res.Len() > 2 ||
			(res.Len() == 2 && !isErrorType(res.At(1).Type())) {
			g.errorf("functions and methods must return either zero or one value, and optionally an error: %s.%s", obj.Name(), m.Name())
			continue
		}

		g.Printf("func (p *proxy%s) %s(", obj.Name(), m.Name())
		for i := 0; i < params.Len(); i++ {
			if i > 0 {
				g.Printf(", ")
			}
			g.Printf("%s %s", paramName(params, i), g.typeString(params.At(i).Type()))
		}
		g.Printf(") ")

		if res.Len() == 1 {
			g.Printf(g.typeString(res.At(0).Type()))
		} else if res.Len() == 2 {
			g.Printf("(%s, error)", g.typeString(res.At(0).Type()))
		}
		g.Printf(" {\n")
		g.Indent()

		g.Printf("in := new(seq.Buffer)\n")
		for i := 0; i < params.Len(); i++ {
			g.genWrite(paramName(params, i), "in", params.At(i).Type())
		}

		if res.Len() == 0 {
			g.Printf("seq.Transact((*seq.Ref)(p), %q, proxy%s_%s_Code, in)\n", ifaceDesc, obj.Name(), m.Name())
		} else {
			g.Printf("out := seq.Transact((*seq.Ref)(p), %q, proxy%s_%s_Code, in)\n", ifaceDesc, obj.Name(), m.Name())
			var rvs []string
			for i := 0; i < res.Len(); i++ {
				rv := fmt.Sprintf("res_%d", i)
				g.genRead(rv, "out", res.At(i).Type())
				rvs = append(rvs, rv)
			}
			g.Printf("return %s\n", strings.Join(rvs, ","))
		}

		g.Outdent()
		g.Printf("}\n\n")
	}
}

func (g *goGen) genRead(valName, seqName string, typ types.Type) {
	if isErrorType(typ) {
		g.Printf("%s := %s.ReadError()\n", valName, seqName)
		return
	}
	switch t := typ.(type) {
	case *types.Pointer:
		switch u := t.Elem().(type) {
		case *types.Named:
			o := u.Obj()
			if o.Pkg() != g.pkg {
				g.errorf("type %s not defined in %s", u, g.pkg)
				return
			}
			g.Printf("// Must be a Go object\n")
			g.Printf("%s_ref := %s.ReadRef()\n", valName, seqName)
			g.Printf("%s := %s_ref.Get().(*%s.%s)\n", valName, valName, g.pkg.Name(), o.Name())
		default:
			g.errorf("unsupported pointer type %s", t)
		}
	case *types.Named:
		switch t.Underlying().(type) {
		case *types.Interface, *types.Pointer:
			hasProxy := true
			if iface, ok := t.Underlying().(*types.Interface); ok {
				hasProxy = makeIfaceSummary(iface).implementable
			}
			o := t.Obj()
			if o.Pkg() != g.pkg {
				g.errorf("type %s not defined in %s", t, g.pkg)
				return
			}
			g.Printf("var %s %s\n", valName, g.typeString(t))
			g.Printf("%s_ref := %s.ReadRef()\n", valName, seqName)
			g.Printf("if %s_ref.Num < 0 { // go object \n", valName)
			g.Printf("   %s = %s_ref.Get().(%s.%s)\n", valName, valName, g.pkg.Name(), o.Name())
			if hasProxy {
				g.Printf("} else {  // foreign object \n")
				g.Printf("   %s = (*proxy%s)(%s_ref)\n", valName, o.Name(), valName)
			}
			g.Printf("}\n")
		default:
			g.errorf("unsupported named type %s", t)
		}
	default:
		g.Printf("%s := %s.Read%s()\n", valName, seqName, seqType(t))
	}
}

func (g *goGen) typeString(typ types.Type) string {
	pkg := g.pkg

	switch t := typ.(type) {
	case *types.Named:
		obj := t.Obj()
		if obj.Pkg() == nil { // e.g. error type is *types.Named.
			return types.TypeString(typ, types.RelativeTo(pkg))
		}
		if obj.Pkg() != g.pkg {
			g.errorf("type %s not defined in %s", t, g.pkg)
		}

		switch t.Underlying().(type) {
		case *types.Interface, *types.Struct:
			return fmt.Sprintf("%s.%s", pkg.Name(), types.TypeString(typ, types.RelativeTo(pkg)))
		default:
			g.errorf("unsupported named type %s / %T", t, t)
		}
	case *types.Pointer:
		switch t := t.Elem().(type) {
		case *types.Named:
			return fmt.Sprintf("*%s", g.typeString(t))
		default:
			g.errorf("not yet supported, pointer type %s / %T", t, t)
		}
	default:
		return types.TypeString(typ, types.RelativeTo(pkg))
	}
	return ""
}

func (g *goGen) gen() error {
	g.genPreamble()

	var funcs, vars []string

	scope := g.pkg.Scope()
	names := scope.Names()

	hasExported := false
	for _, name := range names {
		obj := scope.Lookup(name)
		if !obj.Exported() {
			continue
		}
		hasExported = true

		switch obj := obj.(type) {
		// TODO(crawshaw): case *types.Var:
		case *types.Func:
			// TODO(crawshaw): functions that are not implementable from
			// another language may still be callable.
			if isCallable(obj) {
				g.genFunc(obj)
				funcs = append(funcs, obj.Name())
			}
		case *types.TypeName:
			named := obj.Type().(*types.Named)
			switch T := named.Underlying().(type) {
			case *types.Struct:
				g.genStruct(obj, T)
			case *types.Interface:
				g.genInterface(obj)
			}
		case *types.Var:
			g.genVar(obj)
			vars = append(vars, obj.Name())
		case *types.Const:
		default:
			g.errorf("not yet supported, name for %v / %T", obj, obj)
			continue
		}
	}
	if !hasExported {
		g.errorf("no exported names in the package %q", g.pkg.Path())
	}

	if len(funcs) > 0 {
		g.Printf("func init() {\n")
		g.Indent()
		for i, name := range funcs {
			g.Printf("seq.Register(%q, %d, proxy_%s)\n", g.pkg.Name(), i+1, name)
		}
		g.Outdent()
		g.Printf("}\n")
	}

	if len(vars) > 0 {
		g.Printf("func init() {\n")
		g.Indent()
		for _, name := range vars {
			varDesc := fmt.Sprintf("%s.%s", g.pkg.Name(), name)
			g.Printf("seq.Register(%q, %d, var_set%s)\n", varDesc, 1, name)
			g.Printf("seq.Register(%q, %d, var_get%s)\n", varDesc, 2, name)
		}
		g.Outdent()
		g.Printf("}\n")
	}

	if len(g.err) > 0 {
		return g.err
	}
	return nil
}
