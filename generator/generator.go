package generator

import (
	"fmt"
	"go/types"
	"sort"
	"strings"

	"github.com/dave/jennifer/jen"
	"github.com/jmattheis/goverter/builder"
	"github.com/jmattheis/goverter/comments"
	"github.com/jmattheis/goverter/namer"
	"github.com/jmattheis/goverter/xtype"
	"golang.org/x/tools/go/packages"
)

type methodDefinition struct {
	ID              string
	Explicit        bool
	Name            string
	Call            *jen.Statement
	Source          *xtype.Type
	Target          *xtype.Type
	Mapping         map[string]string
	IgnoredFields   map[string]struct{}
	IdentityMapping map[string]struct{}
	PipeMapping     map[string]string
	ExtendMapping   map[string]*builder.ExtendMethod
	MatchIgnoreCase bool
	WrapErrors      bool

	Jen jen.Code

	SelfAsFirstParam       bool
	ReturnError            bool
	ReturnTypeOrigin       string
	Dirty                  bool
	IgnoreUnexportedFields bool
}

type generator struct {
	namer  *namer.Namer
	name   string
	file   *jen.File
	lookup map[xtype.Signature]*methodDefinition
	extend map[xtype.Signature]*methodDefinition
	// wrapErrors embeds field names in the error msg
	wrapErrors bool
	// pkgCache caches the extend packages, saving load time
	pkgCache map[string][]*packages.Package
	// workingDir is a working directory, can be empty
	workingDir             string
	ignoreUnexportedFields bool
	ignoreCaseGlobal       bool
}

func (g *generator) registerMethod(scope *types.Scope, methodType *types.Func, methodComments comments.Method) error {
	signature, ok := methodType.Type().(*types.Signature)
	if !ok {
		return fmt.Errorf("expected signature %#v", methodType.Type())
	}
	params := signature.Params()
	if params.Len() != 1 {
		return fmt.Errorf("expected signature to have only one parameter")
	}
	result := signature.Results()
	if result.Len() < 1 || result.Len() > 2 {
		return fmt.Errorf("return has no or too many parameters")
	}
	source := params.At(0).Type()
	target := result.At(0).Type()
	returnError := false
	if result.Len() == 2 {
		if i, ok := result.At(1).Type().(*types.Named); ok && i.Obj().Name() == "error" && i.Obj().Pkg() == nil {
			returnError = true
		} else {
			return fmt.Errorf("second return parameter must have type error but had: %s", result.At(1).Type())
		}
	}

	m := &methodDefinition{
		Call:                   jen.Id(xtype.ThisVar).Dot(methodType.Name()),
		ID:                     methodType.String(),
		Explicit:               true,
		Name:                   methodType.Name(),
		Source:                 xtype.TypeOf(source),
		Target:                 xtype.TypeOf(target),
		Mapping:                methodComments.NameMapping,
		MatchIgnoreCase:        methodComments.MatchIgnoreCase,
		IgnoreUnexportedFields: g.ignoreUnexportedFields,
		WrapErrors:             methodComments.WrapErrors,
		IgnoredFields:          methodComments.IgnoredFields,
		IdentityMapping:        methodComments.IdentityMapping,
		ExtendMapping:          map[string]*builder.ExtendMethod{},
		PipeMapping:            map[string]string{},
		ReturnError:            returnError,
		ReturnTypeOrigin:       methodType.FullName(),
	}

	for field, target := range methodComments.ExtendMapping {
		if strings.Contains(field, "@") {
			// sourceName@targetName
			def, err := g.parseMapExtend(scope, target)
			if err != nil {
				return err
			}
			m.ExtendMapping[field] = def
			s := strings.Split(field, "@")
			m.PipeMapping[s[1]] = s[0]
		} else {
			def, err := g.parseMapExtend(scope, target)
			if err != nil {
				return err
			}
			m.ExtendMapping[field] = def
		}
	}

	g.lookup[xtype.Signature{
		Source: source.String(),
		Target: target.String(),
	}] = m
	g.namer.Register(m.Name)
	return nil
}

func (g *generator) createMethods() error {
	methods := []*methodDefinition{}
	for _, method := range g.lookup {
		methods = append(methods, method)
	}
	sort.Slice(methods, func(i, j int) bool {
		return methods[i].Name < methods[j].Name
	})
	for _, method := range methods {
		if method.Jen != nil && !method.Dirty {
			continue
		}
		method.Dirty = false
		err := g.buildMethod(method, builder.NoWrap)
		if err != nil {
			err = err.Lift(&builder.Path{
				SourceID:   "source",
				TargetID:   "target",
				SourceType: method.Source.T.String(),
				TargetType: method.Target.T.String(),
			})
			return fmt.Errorf("Error while creating converter method:\n    %s\n\n%s", method.ID, builder.ToString(err))
		}
	}
	for _, method := range g.lookup {
		if method.Dirty {
			return g.createMethods()
		}
	}
	g.appendToFile()
	return nil
}

func (g *generator) appendToFile() {
	methods := []*methodDefinition{}
	for _, method := range g.lookup {
		methods = append(methods, method)
	}
	sort.Slice(methods, func(i, j int) bool {
		return methods[i].Name < methods[j].Name
	})
	for _, method := range methods {
		g.file.Add(method.Jen)
	}
}

func (g *generator) buildMethod(method *methodDefinition, errWrapper builder.ErrorWrapper) *builder.Error {
	sourceID := jen.Id("source")
	source := method.Source
	target := method.Target

	returns := []jen.Code{target.TypeAsJen()}
	if method.ReturnError {
		returns = append(returns, jen.Id("error"))
	}

	ctx := &builder.MethodContext{
		Namer:                  namer.New(),
		Mapping:                method.Mapping,
		ExtendMapping:          method.ExtendMapping,
		PipeMapping:            method.PipeMapping,
		IgnoredFields:          method.IgnoredFields,
		IgnoreUnexportedFields: method.IgnoreUnexportedFields,
		IdentityMapping:        method.IdentityMapping,
		MatchIgnoreCase:        method.MatchIgnoreCase || g.ignoreCaseGlobal,
		WrapErrors:             method.WrapErrors,
		TargetType:             method.Target,
		Signature:              xtype.Signature{Source: method.Source.T.String(), Target: method.Target.T.String()},
	}

	var stmt []jen.Code
	var newID *xtype.JenID
	var err *builder.Error
	if extendMethod, ok := g.extend[ctx.Signature]; ok {
		stmt, newID, err = g.callMethod(
			ctx, extendMethod, xtype.VariableID(sourceID.Clone()), source, target, errWrapper)
	} else {
		stmt, newID, err = g.buildNoLookup(ctx, xtype.VariableID(sourceID.Clone()), source, target)
	}
	if err != nil {
		return err
	}

	ret := []jen.Code{newID.Code}
	if method.ReturnError {
		ret = append(ret, jen.Nil())
	}

	stmt = append(stmt, jen.Return(ret...))

	method.Jen = jen.Func().Params(jen.Id(xtype.ThisVar).Op("*").Id(g.name)).Id(method.Name).
		Params(jen.Id("source").Add(source.TypeAsJen())).Params(returns...).
		Block(stmt...)

	return nil
}

func (g *generator) buildNoLookup(ctx *builder.MethodContext, sourceID *xtype.JenID, source, target *xtype.Type) ([]jen.Code, *xtype.JenID, *builder.Error) {
	for _, rule := range BuildSteps {
		if rule.Matches(source, target) {
			return rule.Build(g, ctx, sourceID, source, target)
		}
	}
	return nil, nil, builder.NewError(fmt.Sprintf("TypeMismatch: Cannot convert %s to %s", source.T, target.T))
}

func (g *generator) callMethod(
	ctx *builder.MethodContext,
	method *methodDefinition,
	sourceID *xtype.JenID,
	source, target *xtype.Type,
	errWrapper builder.ErrorWrapper,
) ([]jen.Code, *xtype.JenID, *builder.Error) {
	params := []jen.Code{}
	if method.SelfAsFirstParam {
		params = append(params, jen.Id(xtype.ThisVar))
	}
	params = append(params, sourceID.Code.Clone())
	if method.ReturnError {
		current := g.lookup[ctx.Signature]
		if !current.ReturnError {
			if current.Explicit {
				return nil, nil, builder.NewError(fmt.Sprintf("ReturnTypeMismatch: Cannot use\n\n    %s\n\nin\n\n    %s\n\nbecause no error is returned as second parameter", method.ReturnTypeOrigin, current.ID))
			}
			current.ReturnError = true
			current.ReturnTypeOrigin = method.ID
			current.Dirty = true
		}

		name := ctx.Name(target.ID())
		innerName := ctx.Name("errValue")
		stmt := []jen.Code{
			jen.List(jen.Id(name), jen.Id("err")).Op(":=").Add(method.Call.Clone().Call(params...)),
			jen.If(jen.Id("err").Op("!=").Nil()).Block(
				jen.Var().Id(innerName).Add(ctx.TargetType.TypeAsJen()),
				jen.Return(jen.Id(innerName), g.wrap(ctx, errWrapper, jen.Id("err")))),
		}
		return stmt, xtype.VariableID(jen.Id(name)), nil
	}
	id := xtype.OtherID(method.Call.Clone().Call(params...))
	return nil, id, nil
}

// wrap invokes the error wrapper if feature is enabled.
func (g *generator) wrap(ctx *builder.MethodContext, errWrapper builder.ErrorWrapper, errStmt *jen.Statement) *jen.Statement {
	if g.wrapErrors || ctx.WrapErrors {
		return errWrapper(errStmt)
	}
	return errStmt
}

// Build builds an implementation for the given source and target type, or uses an existing method for it.
func (g *generator) Build(
	ctx *builder.MethodContext,
	sourceID *xtype.JenID,
	source, target *xtype.Type,
	errWrapper builder.ErrorWrapper,
) ([]jen.Code, *xtype.JenID, *builder.Error) {
	method, ok := g.extend[xtype.Signature{Source: source.T.String(), Target: target.T.String()}]
	if !ok {
		method, ok = g.lookup[xtype.Signature{Source: source.T.String(), Target: target.T.String()}]
	}

	if ok {
		return g.callMethod(ctx, method, sourceID, source, target, errWrapper)
	}

	if (source.Named && !source.Basic) || (target.Named && !target.Basic) {
		name := g.namer.Name(source.UnescapedID() + "To" + strings.Title(target.UnescapedID()))

		method := &methodDefinition{
			ID:                     name,
			Name:                   name,
			Source:                 xtype.TypeOf(source.T),
			Target:                 xtype.TypeOf(target.T),
			Mapping:                map[string]string{},
			PipeMapping:            map[string]string{},
			IgnoredFields:          map[string]struct{}{},
			IgnoreUnexportedFields: g.ignoreUnexportedFields,
			Call:                   jen.Id(xtype.ThisVar).Dot(name),
		}
		if ctx.PointerChange {
			ctx.PointerChange = false
			method.Mapping = ctx.Mapping
			method.MatchIgnoreCase = ctx.MatchIgnoreCase
			method.WrapErrors = ctx.WrapErrors
			method.IgnoredFields = ctx.IgnoredFields
			method.ExtendMapping = ctx.ExtendMapping
			method.IdentityMapping = ctx.IdentityMapping
		}
		method.PipeMapping = ctx.PipeMapping

		g.lookup[xtype.Signature{Source: source.T.String(), Target: target.T.String()}] = method
		g.namer.Register(method.Name)
		if err := g.buildMethod(method, errWrapper); err != nil {
			return nil, nil, err
		}
		// try again to trigger the found method thingy above
		return g.Build(ctx, sourceID, source, target, errWrapper)
	}

	for _, rule := range BuildSteps {
		if rule.Matches(source, target) {
			return rule.Build(g, ctx, sourceID, source, target)
		}
	}

	return nil, nil, builder.NewError(fmt.Sprintf("TypeMismatch: Cannot convert %s to %s", source.T, target.T))
}
