package agentv3

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"text/template"
	"text/template/parse"

	"github.com/cloudwego/eino/schema"
)

type replySessionTemplateData struct {
	DateTime      string
	CurrentDateCN string
	BotUsername   string
}

func replySessionMetadata(tc *TurnContext) replySessionTemplateData {
	now := beijingNow()
	data := replySessionTemplateData{
		DateTime:      now.Format("2006-01-02 15:04:05"),
		CurrentDateCN: now.Format("2006年01月02日"),
	}
	if tc != nil && tc.BotUser != nil {
		data.BotUsername = tc.BotUser.Username
	}
	return data
}

func validateReplySessionTemplate(tpl *template.Template) error {
	if tpl == nil {
		return nil
	}
	for _, named := range tpl.Templates() {
		if named == nil || named.Tree == nil || named.Tree.Root == nil {
			continue
		}
		if err := validateReplySessionTemplateNode(named.Tree.Root); err != nil {
			return err
		}
	}
	return nil
}

func validateReplySessionTemplateNode(node parse.Node) error {
	if node == nil {
		return nil
	}
	value := reflect.ValueOf(node)
	if value.Kind() == reflect.Pointer && value.IsNil() {
		return nil
	}
	switch node := node.(type) {
	case *parse.ListNode:
		for _, child := range node.Nodes {
			if err := validateReplySessionTemplateNode(child); err != nil {
				return err
			}
		}
	case *parse.ActionNode:
		return validateReplySessionTemplatePipe(node.Pipe)
	case *parse.IfNode:
		return validateReplySessionTemplateBranch(node.Pipe, node.List, node.ElseList)
	case *parse.RangeNode:
		return validateReplySessionTemplateBranch(node.Pipe, node.List, node.ElseList)
	case *parse.WithNode:
		return validateReplySessionTemplateBranch(node.Pipe, node.List, node.ElseList)
	case *parse.TemplateNode:
		return validateReplySessionTemplatePipe(node.Pipe)
	case *parse.PipeNode:
		return validateReplySessionTemplatePipe(node)
	case *parse.CommandNode:
		for _, arg := range node.Args {
			if err := validateReplySessionTemplateNode(arg); err != nil {
				return err
			}
		}
	case *parse.FieldNode:
		return validateReplySessionTemplateFields(node.Ident)
	case *parse.VariableNode:
		if len(node.Ident) > 1 {
			return validateReplySessionTemplateFields(node.Ident[1:])
		}
	case *parse.ChainNode:
		if err := validateReplySessionTemplateNode(node.Node); err != nil {
			return err
		}
		return validateReplySessionTemplateFields(node.Field)
	case *parse.IdentifierNode:
		if node.Ident == "index" || node.Ident == "call" {
			return replySessionTemplateAccessError(node.Ident)
		}
	}
	return nil
}

func validateReplySessionTemplateBranch(pipe *parse.PipeNode, lists ...*parse.ListNode) error {
	if err := validateReplySessionTemplatePipe(pipe); err != nil {
		return err
	}
	for _, list := range lists {
		if err := validateReplySessionTemplateNode(list); err != nil {
			return err
		}
	}
	return nil
}

func validateReplySessionTemplatePipe(pipe *parse.PipeNode) error {
	if pipe == nil {
		return nil
	}
	for _, variable := range pipe.Decl {
		if err := validateReplySessionTemplateNode(variable); err != nil {
			return err
		}
	}
	for _, command := range pipe.Cmds {
		if err := validateReplySessionTemplateNode(command); err != nil {
			return err
		}
	}
	return nil
}

func validateReplySessionTemplateFields(fields []string) error {
	for _, field := range fields {
		switch field {
		case "DateTime", "CurrentDateCN", "BotUsername":
		default:
			return replySessionTemplateAccessError(field)
		}
	}
	return nil
}

var errReplyChainTemplateUnsupportedAccess = errors.New("reply_chain template uses unsupported field/access")

func replySessionTemplateAccessError(access string) error {
	return fmt.Errorf("%w %q; use static text or DateTime, CurrentDateCN, BotUsername; remove conversation interpolation because session messages supply it directly, or use context_mode: chat", errReplyChainTemplateUnsupportedAccess, access)
}

func renderReplySessionTemplate(tpl *template.Template, tc *TurnContext) (string, error) {
	if tpl == nil {
		return "", nil
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, replySessionMetadata(tc)); err != nil {
		return "", fmt.Errorf("render reply_chain template: %w", err)
	}
	return strings.TrimSpace(buf.String()), nil
}

func buildReplySessionPromptAddition(cc *CompiledAgent, tc *TurnContext) (*schema.Message, error) {
	metadata := replySessionMetadata(tc)
	var prompt string
	if cc != nil {
		var err error
		prompt, err = renderReplySessionTemplate(cc.PromptTemplate, tc)
		if err != nil {
			return nil, err
		}
	}
	content := "<reply_session_metadata>\n<datetime>" + metadata.DateTime + "</datetime>"
	if prompt != "" {
		content += "\n" + prompt
	}
	content += "\n</reply_session_metadata>"
	return schema.UserMessage(content), nil
}
