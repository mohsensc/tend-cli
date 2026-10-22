package api

import (
	"strings"
	"testing"
)

func provider(t *testing.T, name string) *Provider {
	t.Helper()
	for _, p := range Catalog() {
		if p.Name == name {
			return &p
		}
	}
	t.Fatalf("no provider %s", name)
	return nil
}

func TestOperation(t *testing.T) {
	stripe, openai, github := provider(t, "stripe"), provider(t, "openai"), provider(t, "github")
	tests := []struct {
		p     *Provider
		chain string
		lang  string
		want  string
	}{
		{stripe, "stripe.charges.create", "js", "POST /v1/charges"},
		{stripe, "stripe.charges.retrieve", "js", "GET /v1/charges/{id}"},
		{stripe, "stripe.charges.update", "js", "POST /v1/charges/{id}"},
		{stripe, "stripe.charges.list", "js", "GET /v1/charges"},
		{stripe, "stripe.customers.del", "js", "DELETE /v1/customers/{id}"},
		{stripe, "stripe.paymentIntents.create", "js", "POST /v1/payment_intents"},
		{stripe, "stripe.paymentIntents.cancel", "js", "POST /v1/payment_intents/{id}/cancel"},
		{stripe, "stripe.paymentIntents.confirm", "js", "POST /v1/payment_intents/{id}/confirm"},
		{stripe, "stripe.checkout.sessions.create", "js", "POST /v1/checkout/sessions"},
		{stripe, "stripe.Charge.create", "python", "POST /v1/charges"},
		{stripe, "stripe.PaymentIntent.modify", "python", "POST /v1/payment_intents/{id}"},
		{stripe, "stripe.Customer.delete", "python", "DELETE /v1/customers/{id}"},
		{stripe, "Charge.create", "python", "POST /v1/charges"},
		{stripe, "Stripe.Charge.create", "ruby", "POST /v1/charges"},
		{stripe, "Stripe.Checkout.Session.create", "ruby", "POST /v1/checkout/sessions"},
		{stripe, "Stripe.Charge.all", "ruby", "GET /v1/charges"},
		{stripe, "Stripe.Invoice.destroy", "ruby", "DELETE /v1/invoices/{id}"},
		{stripe, "Stripe.Address.list", "ruby", "GET /v1/addresses"},
		{stripe, "Stripe.Policy.list", "ruby", "GET /v1/policies"},
		{stripe, "Stripe.Day.list", "ruby", "GET /v1/days"},
		{stripe, "Stripe.Box.list", "ruby", "GET /v1/boxes"},
		{stripe, "Stripe.Batch.list", "ruby", "GET /v1/batches"},
		{stripe, "Charge.create", "java", "POST /v1/charges"},
		{stripe, "charge.New", "go", "POST /v1/charges"},
		{stripe, "charge.Get", "go", "GET /v1/charges/{id}"},
		{stripe, "charge.Update", "go", "POST /v1/charges/{id}"},
		{stripe, "charge.Del", "go", "DELETE /v1/charges/{id}"},
		{stripe, "charge.List", "go", "GET /v1/charges"},
		{stripe, "paymentintent.Capture", "go", "POST /v1/paymentintents/{id}/capture"},
		{openai, "openai.chat.completions.create", "js", "POST /v1/chat/completions"},
		{openai, "client.models.retrieve", "python", "GET /v1/models/{id}"},
		{openai, "openai.files.delete", "js", "DELETE /v1/files/{id}"},
		{github, "octokit.issues.update", "js", "PATCH /issues/{id}"},
		{github, "octokit.issues.send", "js", "POST /issues/{id}/send"},

		{stripe, "stripe.charges.frobnicate", "js", ""},
		{stripe, "stripe.charges", "js", ""},
		{stripe, "stripe", "js", ""},
		{stripe, "stripe.Charge.new", "ruby", ""},
		{stripe, "stripe.webhooks.constructEvent", "js", ""},
		{openai, "openai.chat.completions.update", "js", "PATCH /v1/chat/completions/{id}"},
	}
	for _, tt := range tests {
		t.Run(tt.lang+" "+tt.p.Name+" "+tt.chain, func(t *testing.T) {
			method, path, ok := tt.p.Operation(strings.Split(tt.chain, "."), tt.lang)
			got := ""
			if ok {
				got = method + " " + path
			}
			if got != tt.want {
				t.Errorf("Operation = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOperationNeedsTheSpec(t *testing.T) {
	stripe := provider(t, "stripe")
	d := &Doc{Ops: map[string]*Op{
		"POST /v1/charges":              {Method: "POST", Path: "/v1/charges"},
		"GET /v1/charges/{charge}":      {Method: "GET", Path: "/v1/charges/{charge}"},
		"POST /v1/payment_intents":      {Method: "POST", Path: "/v1/payment_intents"},
		"GET /v1/payment_intents/{pi}":  {Method: "GET", Path: "/v1/payment_intents/{pi}"},
		"POST /v1/payment_intents/{pi}": {Method: "POST", Path: "/v1/payment_intents/{pi}"},
	}}
	tests := []struct {
		chain, lang, want string
	}{
		{"stripe.charges.retrieve", "js", "GET /v1/charges/{charge}"},
		{"Stripe.PaymentIntent.update", "ruby", "POST /v1/payment_intents/{pi}"},
		{"paymentintent.Get", "go", "GET /v1/payment_intents/{pi}"},
		{"stripe.charges.del", "js", ""},
		{"stripe.refunds.create", "js", ""},
	}
	for _, tt := range tests {
		method, path, ok := stripe.Operation(strings.Split(tt.chain, "."), tt.lang)
		got := ""
		if op := d.find(method, path, true); ok && op != nil {
			got = op.Method + " " + op.Path
		}
		if got != tt.want {
			t.Errorf("%s: %q, want %q", tt.chain, got, tt.want)
		}
	}
}

func TestSnake(t *testing.T) {
	tests := map[string]string{
		"charges":        "charges",
		"paymentIntents": "payment_intents",
		"PaymentIntent":  "payment_intent",
		"HTTPClient":     "http_client",
		"v2Items":        "v2_items",
		"already_snake":  "already_snake",
		"Snake_Case":     "snake_case",
		"":               "",
	}
	for in, want := range tests {
		if got := snake(in); got != want {
			t.Errorf("snake(%q) = %q, want %q", in, got, want)
		}
	}
}
