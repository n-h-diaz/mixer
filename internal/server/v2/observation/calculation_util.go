// Copyright 2024 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package observation

import (
	"fmt"
	"go/ast"
	"go/parser"
	"reflect"
	"strings"

	pb "github.com/datacommonsorg/mixer/internal/proto"
	pbv2 "github.com/datacommonsorg/mixer/internal/proto/v2"
)

// The info of a node in the AST tree.
type ASTNode struct {
	StatVar string
	Facet   *pb.Facet
}

type VariableFormula struct {
	Expr ast.Expr
	// Map of leaves in AST tree formula to the corresponding StatVar and Facet.
	// The key is encodeForParse(nodeName), where nodeName contains the StatVar dcid and filters,
	// (for example: "Count_Person[mm=US_Census;p=P1Y]").
	LeafData map[string]*ASTNode
	// List of distinct StatVars in the formula.
	StatVars []string
}

// Golang's AST package is used for parsing the formula, so we need to avoid sensitive tokens for
// AST. For those tokens, we swap them with insensitive tokens before the parsing, then swap them
// back after the parsing.
var (
	encodeForParseTokenMap = map[string]string{
		"dc/":          "_DC_SLASH_",
		"dcAggregate/": "_DC_AGGREGATE_SLASH_",
		"[":            "_LEFT_SQUARE_BRACKET_",
		"]":            "_RIGHT_SQUARE_BRACKET_",
		"=":            "_EQUAL_TO_",
		";":            "_SEMICOLON_",
	}
)

func encodeForParse(s string) string {
	res := s
	for k, v := range encodeForParseTokenMap {
		res = strings.ReplaceAll(res, k, v)
	}
	return res
}

func decodeForParse(s string) string {
	res := s
	for k, v := range encodeForParseTokenMap {
		res = strings.ReplaceAll(res, v, k)
	}
	return res

}

// Parse nodeName, which contains a variable and a set of filters.
// For example: Count_Person[mm=US_Census;p=P1Y].
func parseNode(nodeName string) (*ASTNode, error) {
	res := &ASTNode{}

	if strings.Contains(nodeName, "[") { // With filters.
		if !strings.Contains(nodeName, "]") {
			return nil, fmt.Errorf("missing ]")
		}

		leftBracketIndex := strings.Index(nodeName, "[")

		res.Facet = &pb.Facet{}
		filterString := nodeName[leftBracketIndex+1 : len(nodeName)-1]
		for _, filter := range strings.Split(filterString, ";") {
			filterType := filter[0:2]
			filterVal := filter[3:]
			switch filterType {
			case "mm":
				res.Facet.MeasurementMethod = filterVal
			case "op":
				res.Facet.ObservationPeriod = filterVal
			case "ut":
				res.Facet.Unit = filterVal
			case "sf":
				res.Facet.ScalingFactor = filterVal
			default:
				return nil, fmt.Errorf("unsupported filter type: %s", filterType)
			}
		}

		res.StatVar = nodeName[0:leftBracketIndex]

	} else { // No filters.
		res.StatVar = nodeName
	}

	return res, nil
}

func NewVariableFormula(formula string) (*VariableFormula, error) {
	expr, err := parser.ParseExpr(encodeForParse(formula))
	if err != nil {
		return nil, err
	}

	c := &VariableFormula{Expr: expr, LeafData: map[string]*ASTNode{}}
	if err := processNodeInfo(expr, c); err != nil {
		return nil, err
	}

	statVarSet := map[string]struct{}{}
	for k := range c.LeafData {
		statVarSet[c.LeafData[k].StatVar] = struct{}{}
	}
	statVars := []string{}
	for k := range statVarSet {
		statVars = append(statVars, k)
	}
	c.StatVars = statVars

	return c, nil
}

// Recursively iterate through the AST tree, extract and parse nodeName, then fill nodeData.
func processNodeInfo(node ast.Node, c *VariableFormula) error {
	switch t := node.(type) {
	case *ast.BinaryExpr:
		for _, node := range []ast.Node{t.X, t.Y} {
			if reflect.TypeOf(node).String() == "*ast.Ident" {
				nodeName := node.(*ast.Ident).Name
				nodeData, err := parseNode(decodeForParse(nodeName))
				if err != nil {
					return err
				}
				c.LeafData[nodeName] = nodeData
			} else {
				if err := processNodeInfo(node, c); err != nil {
					return err
				}
			}
		}
	case *ast.ParenExpr:
		return processNodeInfo(t.X, c)
	default:
		return fmt.Errorf("unsupported AST type %T", t)
	}

	return nil
}

// Given an input ObservationResponse, generate a map of variable -> entities with missing data.
func findObservationResponseHoles(
	inputReq *pbv2.ObservationRequest,
	inputResp *pbv2.ObservationResponse,
) (map[string]*pbv2.DcidOrExpression, error) {
	result := map[string]*pbv2.DcidOrExpression{}
	if inputReq.Variable.GetFormula() != "" {
		return nil, fmt.Errorf("currently do not support nested formulas")
	}
	for variable, variableObs := range inputResp.ByVariable {
		if len(inputReq.Entity.GetDcids()) > 0 {
			holeEntities := []string{}
			for entity, entityObs := range variableObs.ByEntity {
				if len(entityObs.OrderedFacets) == 0 {
					holeEntities = append(holeEntities, entity)
				}
			}
			if len(holeEntities) > 0 {
				result[variable] = &pbv2.DcidOrExpression{Dcids: holeEntities}
			}
		} else if inputReq.Entity.GetExpression() != "" {
			if len(variableObs.ByEntity) == 0 {
				result[variable] = &pbv2.DcidOrExpression{Expression: inputReq.Entity.Expression}
			}
		}
	}
	return result, nil
}
