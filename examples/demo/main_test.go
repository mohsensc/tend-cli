package main

import "testing"

func TestKeyed(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"none", nil, false},
		{"tend's own", map[string]string{"TEND_MODEL_KEY": "k"}, true},
		{"the provider's", map[string]string{"GROQ_API_KEY": "k"}, true},
		{"blank", map[string]string{"TEND_MODEL_KEY": "  "}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := keyed(func(k string) string { return tt.env[k] }); got != tt.want {
				t.Errorf("keyed = %v, want %v", got, tt.want)
			}
		})
	}
}
