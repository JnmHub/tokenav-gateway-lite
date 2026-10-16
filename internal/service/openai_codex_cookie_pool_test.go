package service

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCaptureOpenAICodexRouteCookies_AccountPoolAndExpiry(t *testing.T) {
	s := &OpenAIGatewayService{}
	account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	header := http.Header{}
	header.Add("Set-Cookie", "__cfblb=cf-1; Path=/; Expires="+time.Now().Add(10*time.Minute).UTC().Format(http.TimeFormat))
	header.Add("Set-Cookie", "__oailb=oa-1; Path=/")
	header.Add("Set-Cookie", "other=skip; Path=/")

	s.captureOpenAICodexRouteCookies(account, header)
	got := s.openAICodexRouteCookieHeader(account)
	require.Equal(t, "__cfblb=cf-1; __oailb=oa-1", got)

	statuses := OpenAICodexRouteCookieStatuses(&Account{
		ID:       account.ID,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			openAICodexRouteCookieExtraKey: s.lookupOpenAICodexRouteCookiePool(account),
		},
	})
	require.Len(t, statuses, 2)
	require.True(t, statuses[0].Ready)
	require.Equal(t, "__cfblb", statuses[0].Name)
	require.True(t, statuses[1].Ready)
	require.Equal(t, "__oailb", statuses[1].Name)
}

func TestOpenAICodexRouteCookieHeader_SkipsExpired(t *testing.T) {
	s := &OpenAIGatewayService{}
	account := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	s.openaiCodexRouteCookies.Store(account.ID, openAICodexRouteCookiePool{
		"__cfblb": {Value: "dead", ExpiresAt: time.Now().Add(-time.Minute)},
		"__oailb": {Value: "live"},
	})
	require.Equal(t, "__oailb=live", s.openAICodexRouteCookieHeader(account))
}

func TestNoteOpenAICodexTurnStateRefresh_RollsQualifiedTicket(t *testing.T) {
	s := &OpenAIGatewayService{}
	account := &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	state := openAICodexTicketStatePrefix + "A"
	for len(state) < 292 {
		state += "x"
	}
	state = state[:292]

	s.noteOpenAICodexTurnStateRefresh(account, "gpt-6-astra", state)
	ticket := s.lookupOpenAICodexTicket(account, "gpt-6-astra")
	require.True(t, ticket.valid(time.Now(), 292))
	require.Equal(t, state, ticket.State)
	require.WithinDuration(t, time.Now().Add(240*time.Second), ticket.ExpiresAt, 2*time.Second)

	s.noteOpenAICodexTurnStateRefresh(account, "gpt-6-astra", state+"y")
	require.Equal(t, state, s.lookupOpenAICodexTicket(account, "gpt-6-astra").State)
}
