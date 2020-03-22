package module

import "fmt"

type Context struct {
	namespace string
}

func NewContext(namespace string) Context {
	return Context{namespace: namespace}
}

func (ctx Context) WrapKey(key string) string {
	return fmt.Sprintf("%s:%s", ctx.namespace, key)
}

func (ctx Context) SubContext(sub string) Context {
	return NewContext(ctx.WrapKey(sub))
}

func GlobalContext() Context {
	return Context{namespace: ""}
}
