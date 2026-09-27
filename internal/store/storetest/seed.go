//go:build integration

package storetest

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/elqsar/better-api-portal/internal/index"
	"github.com/elqsar/better-api-portal/internal/model"
	"github.com/elqsar/better-api-portal/internal/store"
)

// Synthetic APIs for search benchmarks: domain × noun names, so words like
// "refund" appear in many APIs and kinds, as in a real catalogue.
var (
	domains = []string{"orders", "payments", "refunds", "shipping", "inventory", "customers", "invoices",
		"catalog", "pricing", "loyalty", "returns", "subscriptions", "notifications", "accounts", "fraud",
		"ledger", "warehouse", "carts", "promotions", "reviews"}
	nouns = []string{"order", "payment", "refund", "shipment", "stock item", "customer", "invoice", "product",
		"price", "reward", "return", "subscription", "message", "account", "risk score", "journal entry",
		"pallet", "cart", "coupon", "review"}
	verbs = []string{"created", "updated", "cancelled", "approved", "rejected", "completed", "failed",
		"expired", "archived", "restored"}
	filler = strings.Repeat("The service keeps an audit trail of every change and retries delivery "+
		"with exponential backoff; consumers must be idempotent and tolerate reordering. ", 12)
)

// SeedAPIs publishes n synthetic APIs, alternately OpenAPI and CloudEvents,
// each with an older version below its latest. Every tenth is deprecated
// and every twenty-fifth retired. API i is "<domain>-<i>".
func SeedAPIs(tb testing.TB, s *store.Store, n int) {
	tb.Helper()
	ctx := context.Background()
	for i := range n {
		domain := domains[i%len(domains)]
		id := fmt.Sprintf("%s-%d", domain, i)
		repo, err := s.Repo(ctx, "acme/"+id)
		if err != nil {
			tb.Fatal(err)
		}
		lifecycle := "production"
		switch {
		case i%25 == 24:
			lifecycle = "retired"
		case i%10 == 9:
			lifecycle = "deprecated"
		}
		kind := "openapi"
		if i%2 == 1 {
			kind = "cloudevents"
		}
		for _, v := range []string{"1.0.0", "1.1.0"} {
			spec := synthSpec(i, kind, domain, v)
			p := store.Push{
				API:    store.API{ID: id, Kind: kind, Owner: "team-" + domain, Lifecycle: lifecycle},
				RepoID: repo,
				Actor:  "seed",
				Version: store.Version{
					Semver: v, ContentHash: "sha256:" + id + "-" + v, Status: store.StatusPublished,
					Source: store.Source{Repo: "acme/" + id, PushedBy: "seed", PushedAt: time.Unix(0, 0).UTC()},
				},
				Bundle: []byte(id + v),
				Score:  90,
				Index:  index.Build(index.API{ID: id, Title: strings.ToUpper(domain[:1]) + domain[1:] + " service"}, spec),
			}
			if _, err := s.Record(ctx, p); err != nil {
				tb.Fatalf("seed %s %s: %v", id, v, err)
			}
		}
	}
}

// synthSpec has 20 operations or messages and 10 schemas.
func synthSpec(i int, kind, domain, version string) *model.Spec {
	spec := &model.Spec{Kind: kind, Title: domain, Version: version,
		Description: fmt.Sprintf("Owns the %s domain. %s", domain, filler),
		Schemas:     map[string]*model.Schema{}}
	for j := range 20 {
		noun := nouns[(i+j)%len(nouns)]
		verb := verbs[j%len(verbs)]
		slug := strings.ReplaceAll(noun, " ", "-")
		if kind == "openapi" {
			spec.Operations = append(spec.Operations, model.Operation{
				Method: []string{"GET", "POST", "PUT", "DELETE"}[j%4], Path: fmt.Sprintf("/%s/{id}/%ss/%d", domain, slug, j),
				OperationID: fmt.Sprintf("%s%s%d", verb, strings.ReplaceAll(noun, " ", ""), j),
				Summary:     fmt.Sprintf("Mark a %s as %s", noun, verb),
				Description: fmt.Sprintf("Marks the %s as %s. %s", noun, verb, filler[:300]),
				Tags:        []string{domain},
			})
		} else {
			key := fmt.Sprintf("com.acme.%s.%s.%s.v%d", domain, strings.ReplaceAll(noun, " ", ""), verb, 1+j/10)
			spec.Messages = append(spec.Messages, model.Message{
				Key: key, Role: model.RoleProduces, Summary: fmt.Sprintf("A %s was %s", noun, verb),
				Description: fmt.Sprintf("Emitted when a %s is %s. %s", noun, verb, filler[:300]),
				Bindings:    []model.Binding{{Protocol: "kafka", Address: domain + ".events"}},
			})
		}
	}
	for j := range 10 {
		noun := nouns[(i+2*j)%len(nouns)]
		name := pascal(noun) + fmt.Sprintf("V%d", j)
		spec.Schemas["#/components/schemas/"+name] = &model.Schema{Pointer: "#/components/schemas/" + name, Doc: map[string]any{
			"type":        "object",
			"title":       name,
			"description": fmt.Sprintf("A %s in the %s domain.", noun, domain),
			"properties": map[string]any{
				"id":     map[string]any{"type": "string", "description": "Identifier of the " + noun},
				"status": map[string]any{"type": "string", "enum": []any{"open", "closed", "refunded"}},
				"amount": map[string]any{"type": "integer", "description": "Minor units"},
			},
		}}
	}
	return spec
}

func pascal(s string) string {
	var b strings.Builder
	for _, w := range strings.Fields(s) {
		b.WriteString(strings.ToUpper(w[:1]) + w[1:])
	}
	return b.String()
}
