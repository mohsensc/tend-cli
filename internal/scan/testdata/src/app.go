//go:build linux || darwin

package app

/*
#include <stdlib.h>
*/
import "C"

import (
	"context"
	"fmt"
	"net/http"

	_ "github.com/lib/pq"
	. "github.com/onsi/gomega"
	"github.com/sashabaranov/go-openai"
	stripe "github.com/stripe/stripe-go/v79"
	"github.com/stripe/stripe-go/v79/charge"
	"gopkg.in/yaml.v3"
)

type Service struct {
	ai  *openai.Client
	log fmt.Stringer
}

func (s *Service) Ask(ctx context.Context) (string, error) {
	resp, err := s.ai.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: openai.GPT4o,
	})
	if err != nil {
		return "", err
	}
	return resp.Choices[0].Message.Content, nil
}

func Charge(key string, c *openai.Client) error {
	stripe.Key = key
	ch, err := charge.New(&stripe.ChargeParams{Amount: stripe.Int64(2000)})
	if err != nil {
		return err
	}
	fmt.Println(ch.Paid) // charge.Get(ch.ID) in a comment is not a site
	_, _ = c.ListModels(context.Background())
	Expect(ch.Paid).To(BeTrue())
	req, _ := http.NewRequest(http.MethodPost, "https://api.stripe.com/v1/refunds", nil)
	_ = req
	var out map[string]any
	_ = yaml.Unmarshal([]byte("a: 1"), &out)
	C.free(nil)
	return nil
}

func Other(c int) int {
	return c.Value
}
