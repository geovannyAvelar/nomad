// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package jobspec2

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hcldec"
	"github.com/hashicorp/nomad/api"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/gocty"
)

var hclDecoder *gohcl.Decoder

func init() {
	hclDecoder = newHCLDecoder()
	hclDecoder.RegisterBlockDecoder(reflect.TypeOf(api.TaskGroup{}), decodeTaskGroup)
	hclDecoder.RegisterBlockDecoder(reflect.TypeOf(api.Task{}), decodeTask)
}

func newHCLDecoder() *gohcl.Decoder {
	decoder := &gohcl.Decoder{}

	// time conversion
	d := time.Duration(0)
	decoder.RegisterExpressionDecoder(reflect.TypeOf(d), decodeDuration)
	decoder.RegisterExpressionDecoder(reflect.TypeOf(&d), decodeDuration)

	// custom nomad types
	decoder.RegisterBlockDecoder(reflect.TypeOf(api.Affinity{}), decodeAffinity)
	decoder.RegisterBlockDecoder(reflect.TypeOf(api.Constraint{}), decodeConstraint)

	return decoder
}

func decodeDuration(expr hcl.Expression, ctx *hcl.EvalContext, val interface{}) hcl.Diagnostics {
	srcVal, diags := expr.Value(ctx)
	if srcVal.IsNull() {
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Unsuitable value",
			Detail:   "Unsuitable duration value: nil",
			Subject:  expr.StartRange().Ptr(),
			Context:  expr.Range().Ptr(),
		})
		return diags
	}

	if srcVal.Type() == cty.String {
		dur, err := time.ParseDuration(srcVal.AsString())

		if err != nil {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Unsuitable value type",
				Detail:   fmt.Sprintf("Unsuitable duration value: %s", err.Error()),
				Subject:  expr.StartRange().Ptr(),
				Context:  expr.Range().Ptr(),
			})
			return diags
		}

		srcVal = cty.NumberIntVal(int64(dur))
	}

	if srcVal.Type() != cty.Number {
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Unsuitable value type",
			Detail:   fmt.Sprintf("Unsuitable value: expected a string but found %s", srcVal.Type()),
			Subject:  expr.StartRange().Ptr(),
			Context:  expr.Range().Ptr(),
		})
		return diags
	}

	err := gocty.FromCtyValue(srcVal, val)
	if err != nil {
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Unsuitable value type",
			Detail:   fmt.Sprintf("Unsuitable value: %s", err.Error()),
			Subject:  expr.StartRange().Ptr(),
			Context:  expr.Range().Ptr(),
		})
	}
	return diags
}

var affinitySpec = hcldec.ObjectSpec{
	"attribute": &hcldec.AttrSpec{Name: "attribute", Type: cty.String, Required: false},
	"value":     &hcldec.AttrSpec{Name: "value", Type: cty.String, Required: false},
	"operator":  &hcldec.AttrSpec{Name: "operator", Type: cty.String, Required: false},
	"weight":    &hcldec.AttrSpec{Name: "weight", Type: cty.Number, Required: false},

	api.ConstraintVersion:        &hcldec.AttrSpec{Name: api.ConstraintVersion, Type: cty.String, Required: false},
	api.ConstraintSemver:         &hcldec.AttrSpec{Name: api.ConstraintSemver, Type: cty.String, Required: false},
	api.ConstraintRegex:          &hcldec.AttrSpec{Name: api.ConstraintRegex, Type: cty.String, Required: false},
	api.ConstraintSetContains:    &hcldec.AttrSpec{Name: api.ConstraintSetContains, Type: cty.String, Required: false},
	api.ConstraintSetContainsAll: &hcldec.AttrSpec{Name: api.ConstraintSetContainsAll, Type: cty.String, Required: false},
	api.ConstraintSetContainsAny: &hcldec.AttrSpec{Name: api.ConstraintSetContainsAny, Type: cty.String, Required: false},
}

func decodeAffinity(body hcl.Body, ctx *hcl.EvalContext, val interface{}) hcl.Diagnostics {
	a := val.(*api.Affinity)
	v, diags := hcldec.Decode(body, affinitySpec, ctx)
	if len(diags) != 0 {
		return diags
	}

	attr := func(attr string) string {
		a := v.GetAttr(attr)
		if a.IsNull() {
			return ""
		}
		return a.AsString()
	}
	a.LTarget = attr("attribute")
	a.RTarget = attr("value")
	a.Operand = attr("operator")
	weight := v.GetAttr("weight")
	if !weight.IsNull() {
		w, _ := weight.AsBigFloat().Int64()
		a.Weight = pointerOf(int8(w))
	}

	// If "version" is provided, set the operand
	// to "version" and the value to the "RTarget"
	if affinity := attr(api.ConstraintVersion); affinity != "" {
		a.Operand = api.ConstraintVersion
		a.RTarget = affinity
	}

	// If "semver" is provided, set the operand
	// to "semver" and the value to the "RTarget"
	if affinity := attr(api.ConstraintSemver); affinity != "" {
		a.Operand = api.ConstraintSemver
		a.RTarget = affinity
	}

	// If "regexp" is provided, set the operand
	// to "regexp" and the value to the "RTarget"
	if affinity := attr(api.ConstraintRegex); affinity != "" {
		a.Operand = api.ConstraintRegex
		a.RTarget = affinity
	}

	// If "set_contains_any" is provided, set the operand
	// to "set_contains_any" and the value to the "RTarget"
	if affinity := attr(api.ConstraintSetContainsAny); affinity != "" {
		a.Operand = api.ConstraintSetContainsAny
		a.RTarget = affinity
	}

	// If "set_contains_all" is provided, set the operand
	// to "set_contains_all" and the value to the "RTarget"
	if affinity := attr(api.ConstraintSetContainsAll); affinity != "" {
		a.Operand = api.ConstraintSetContainsAll
		a.RTarget = affinity
	}

	// set_contains is a synonym of set_contains_all
	if affinity := attr(api.ConstraintSetContains); affinity != "" {
		a.Operand = api.ConstraintSetContains
		a.RTarget = affinity
	}

	if a.Operand == "" {
		a.Operand = "="
	}
	return diags
}

var constraintSpec = hcldec.ObjectSpec{
	"attribute": &hcldec.AttrSpec{Name: "attribute", Type: cty.String, Required: false},
	"value":     &hcldec.AttrSpec{Name: "value", Type: cty.String, Required: false},
	"operator":  &hcldec.AttrSpec{Name: "operator", Type: cty.String, Required: false},

	api.ConstraintDistinctProperty:  &hcldec.AttrSpec{Name: api.ConstraintDistinctProperty, Type: cty.String, Required: false},
	api.ConstraintDistinctHosts:     &hcldec.AttrSpec{Name: api.ConstraintDistinctHosts, Type: cty.Bool, Required: false},
	api.ConstraintRegex:             &hcldec.AttrSpec{Name: api.ConstraintRegex, Type: cty.String, Required: false},
	api.ConstraintVersion:           &hcldec.AttrSpec{Name: api.ConstraintVersion, Type: cty.String, Required: false},
	api.ConstraintSemver:            &hcldec.AttrSpec{Name: api.ConstraintSemver, Type: cty.String, Required: false},
	api.ConstraintSetContains:       &hcldec.AttrSpec{Name: api.ConstraintSetContains, Type: cty.String, Required: false},
	api.ConstraintSetContainsAll:    &hcldec.AttrSpec{Name: api.ConstraintSetContainsAll, Type: cty.String, Required: false},
	api.ConstraintSetContainsAny:    &hcldec.AttrSpec{Name: api.ConstraintSetContainsAny, Type: cty.String, Required: false},
	api.ConstraintAttributeIsSet:    &hcldec.AttrSpec{Name: api.ConstraintAttributeIsSet, Type: cty.String, Required: false},
	api.ConstraintAttributeIsNotSet: &hcldec.AttrSpec{Name: api.ConstraintAttributeIsNotSet, Type: cty.String, Required: false},
}

func decodeConstraint(body hcl.Body, ctx *hcl.EvalContext, val interface{}) hcl.Diagnostics {
	c := val.(*api.Constraint)

	v, diags := hcldec.Decode(body, constraintSpec, ctx)
	if len(diags) != 0 {
		return diags
	}

	attr := func(attr string) string {
		a := v.GetAttr(attr)
		if a.IsNull() {
			return ""
		}
		return a.AsString()
	}

	c.LTarget = attr("attribute")
	c.RTarget = attr("value")
	c.Operand = attr("operator")

	// If "version" is provided, set the operand
	// to "version" and the value to the "RTarget"
	if constraint := attr(api.ConstraintVersion); constraint != "" {
		c.Operand = api.ConstraintVersion
		c.RTarget = constraint
	}

	// If "semver" is provided, set the operand
	// to "semver" and the value to the "RTarget"
	if constraint := attr(api.ConstraintSemver); constraint != "" {
		c.Operand = api.ConstraintSemver
		c.RTarget = constraint
	}

	// If "regexp" is provided, set the operand
	// to "regexp" and the value to the "RTarget"
	if constraint := attr(api.ConstraintRegex); constraint != "" {
		c.Operand = api.ConstraintRegex
		c.RTarget = constraint
	}

	// If "set_contains" is provided, set the operand
	// to "set_contains" and the value to the "RTarget"
	if constraint := attr(api.ConstraintSetContains); constraint != "" {
		c.Operand = api.ConstraintSetContains
		c.RTarget = constraint
	}

	// The shortcut form of the distinct_hosts constraint is a cty.Bool
	// so it can not use the `attr` func defined earlier
	if d := v.GetAttr(api.ConstraintDistinctHosts); !d.IsNull() {
		c.Operand = api.ConstraintDistinctHosts
		c.RTarget = fmt.Sprint(d.True())
	}

	if property := attr(api.ConstraintDistinctProperty); property != "" {
		c.Operand = api.ConstraintDistinctProperty
		c.LTarget = property
	}

	if c.Operand == "" {
		c.Operand = "="
	}
	return diags
}

func decodeTaskGroup(body hcl.Body, ctx *hcl.EvalContext, val interface{}) hcl.Diagnostics {
	tg := val.(*api.TaskGroup)

	var diags hcl.Diagnostics

	metaAttr, body, moreDiags := decodeAsAttribute(body, ctx, "meta")
	diags = append(diags, moreDiags...)

	tgExtra := struct {
		Vault *api.Vault `hcl:"vault,block"`
	}{}

	extra, _ := gohcl.ImpliedBodySchema(tgExtra)
	content, tgBody, moreDiags := body.PartialContent(extra)
	diags = append(diags, moreDiags...)
	if len(diags) != 0 {
		return diags
	}

	for _, b := range content.Blocks {
		if b.Type == "vault" {
			v := &api.Vault{}
			diags = append(diags, hclDecoder.DecodeBody(b.Body, ctx, v)...)
			tgExtra.Vault = v
		}
	}

	d := newHCLDecoder()
	d.RegisterBlockDecoder(reflect.TypeOf(api.Task{}), decodeTask)
	diags = d.DecodeBody(tgBody, ctx, tg)

	if metaAttr != nil {
		tg.Meta = metaAttr
	}

	if tgExtra.Vault != nil {
		for _, t := range tg.Tasks {
			if t.Vault == nil {
				t.Vault = tgExtra.Vault
			}
		}
	}

	if tg.Scaling != nil {
		if tg.Scaling.Type == "" {
			tg.Scaling.Type = "horizontal"
		}
		diags = append(diags, validateGroupScalingPolicy(tg.Scaling, tgBody)...)
	}
	return diags

}

func decodeTask(body hcl.Body, ctx *hcl.EvalContext, val interface{}) hcl.Diagnostics {
	// special case scaling policy
	t := val.(*api.Task)

	var diags hcl.Diagnostics

	// special case env and meta
	envAttr, body, moreDiags := decodeAsAttribute(body, ctx, "env")
	diags = append(diags, moreDiags...)
	metaAttr, body, moreDiags := decodeAsAttribute(body, ctx, "meta")
	diags = append(diags, moreDiags...)

	b, remain, moreDiags := body.PartialContent(&hcl.BodySchema{
		Blocks: []hcl.BlockHeaderSchema{
			{Type: "scaling", LabelNames: []string{"name"}},
		},
	})

	diags = append(diags, moreDiags...)
	diags = append(diags, decodeTaskScalingPolicies(b.Blocks, ctx, t)...)

	decoder := newHCLDecoder()
	diags = append(diags, decoder.DecodeBody(remain, ctx, val)...)

	if envAttr != nil {
		t.Env = envAttr
	}
	if metaAttr != nil {
		t.Meta = metaAttr
	}

	return diags
}

// decodeAsAttribute decodes the named field as an attribute assignment if found.
//
// Nomad jobs contain attributes (e.g. `env`, `meta`) that are meant to contain arbitrary
// keys. HCLv1 allowed both block syntax (the preferred and documented one) as well as attribute
// assignment syntax:
//
// ```hcl
// # block assignment
//
//	env {
//	  ENV = "production"
//	}
//
// # as attribute
// env = { ENV: "production" }
// ```
//
// HCLv2 block syntax, though, restricts valid input and doesn't allow dots or invalid identifiers
// as block attribute keys.
// Thus, we support both syntax to unrestrict users.
//
// This function attempts to read the named field, as an attribute, and returns
// found map, the remaining body and diagnostics. If the named field is found
// with block syntax, it returns a nil map, and caller falls back to reading
// with block syntax.
func decodeAsAttribute(body hcl.Body, ctx *hcl.EvalContext, name string) (map[string]string, hcl.Body, hcl.Diagnostics) {
	b, remain, diags := body.PartialContent(&hcl.BodySchema{
		Attributes: []hcl.AttributeSchema{
			{Name: name, Required: false},
		},
	})

	if diags.HasErrors() || b.Attributes[name] == nil {
		// ignoring errors, to avoid duplicate errors. True errors will
		// reported in the fallback path
		return nil, body, nil
	}

	attr := b.Attributes[name]

	if attr != nil {
		// check if there is another block
		bb, _, _ := remain.PartialContent(&hcl.BodySchema{
			Blocks: []hcl.BlockHeaderSchema{{Type: name}},
		})
		if len(bb.Blocks) != 0 {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  fmt.Sprintf("Duplicate %v block", name),
				Detail: fmt.Sprintf("%v may not be defined more than once. Another definition is defined at %s.",
					name, attr.Range.String()),
				Subject: &bb.Blocks[0].DefRange,
			})
			return nil, remain, diags
		}
	}

	envExpr := attr.Expr

	result := map[string]string{}
	diags = append(diags, hclDecoder.DecodeExpression(envExpr, ctx, &result)...)

	return result, remain, diags
}

func decodeTaskScalingPolicies(blocks hcl.Blocks, ctx *hcl.EvalContext, task *api.Task) hcl.Diagnostics {
	if len(blocks) == 0 {
		return nil
	}

	var diags hcl.Diagnostics
	seen := map[string]*hcl.Block{}
	for _, b := range blocks {
		label := strings.ToLower(b.Labels[0])
		var policyType string
		switch label {
		case "cpu":
			policyType = "vertical_cpu"
		case "mem":
			policyType = "vertical_mem"
		default:
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid scaling policy name",
				Detail:   `scaling policy name must be "cpu" or "mem"`,
				Subject:  &b.LabelRanges[0],
			})
			continue
		}

		if prev, ok := seen[label]; ok {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  fmt.Sprintf("Duplicate scaling %q block", label),
				Detail: fmt.Sprintf(
					"Only one scaling %s block is allowed. Another was defined at %s.",
					label, prev.DefRange.String(),
				),
				Subject: &b.DefRange,
			})
			continue
		}
		seen[label] = b

		var p api.ScalingPolicy
		diags = append(diags, hclDecoder.DecodeBody(b.Body, ctx, &p)...)

		if p.Type == "" {
			p.Type = policyType
		} else if p.Type != policyType {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid scaling policy type",
				Detail: fmt.Sprintf(
					"Invalid policy type, expected %q but found %q",
					p.Type, policyType),
				Subject: &b.DefRange,
			})
			continue
		}

		task.ScalingPolicies = append(task.ScalingPolicies, &p)
	}

	return diags
}

func validateGroupScalingPolicy(p *api.ScalingPolicy, body hcl.Body) hcl.Diagnostics {
	// fast path: do nothing
	if p.Max != nil && p.Type == "horizontal" {
		return nil
	}

	content, _, diags := body.PartialContent(&hcl.BodySchema{
		Blocks: []hcl.BlockHeaderSchema{{Type: "scaling"}},
	})

	if len(content.Blocks) == 0 {
		// unexpected, given that we have a scaling policy
		return diags
	}

	pc, _, diags := content.Blocks[0].Body.PartialContent(&hcl.BodySchema{
		Attributes: []hcl.AttributeSchema{
			{Name: "max", Required: true},
			{Name: "type", Required: false},
		},
	})

	if p.Type != "horizontal" {
		if attr, ok := pc.Attributes["type"]; ok {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid group scaling type",
				Detail: fmt.Sprintf(
					"task group scaling policy had invalid type: %q",
					p.Type),
				Subject: attr.Expr.Range().Ptr(),
			})
		}
	}
	return diags
}
