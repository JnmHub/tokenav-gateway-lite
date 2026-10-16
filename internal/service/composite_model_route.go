package service

import (
	"context"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	CompositeRouteResolverGinContextKey = "gateway.composite_route_resolver"

	CompositeRouteMatchExact  = "exact"
	CompositeRouteMatchPrefix = "prefix"

	CompositeRouteEndpointAny             = "any"
	CompositeRouteEndpointMessages        = "messages"
	CompositeRouteEndpointCountTokens     = "count_tokens"
	CompositeRouteEndpointResponses       = "responses"
	CompositeRouteEndpointChatCompletions = "chat_completions"
	CompositeRouteEndpointEmbeddings      = "embeddings"
	CompositeRouteEndpointImages          = "images"
	CompositeRouteEndpointGemini          = "gemini"

	CompositeRouteSourceExplicit = "route"
	CompositeRouteSourceDetector = "detector"
)

var (
	ErrCompositeRouteNotFound  = infraerrors.NotFound("COMPOSITE_ROUTE_NOT_FOUND", "composite route not found")
	ErrCompositeRouteExists    = infraerrors.Conflict("COMPOSITE_ROUTE_EXISTS", "composite route already exists")
	ErrCompositeGroupsDisabled = infraerrors.New(503, "COMPOSITE_GROUPS_DISABLED", "composite groups are disabled on this gateway")
)

type CompositeModelRoute struct {
	ID             int64     `json:"id"`
	GroupID        int64     `json:"group_id"`
	PublicModel    string    `json:"public_model"`
	MatchType      string    `json:"match_type"`
	TargetPlatform string    `json:"target_platform"`
	UpstreamModel  string    `json:"upstream_model"`
	Endpoint       string    `json:"endpoint"`
	Priority       int       `json:"priority"`
	Enabled        bool      `json:"enabled"`
	Notes          string    `json:"notes"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type CompositeRoutePreviewRequest struct {
	Model          string               `json:"model"`
	Endpoint       string               `json:"endpoint"`
	Candidate      *CompositeRouteInput `json:"candidate,omitempty"`
	EditingRouteID int64                `json:"editing_route_id,omitempty"`
}

type CompositeRouteDecision struct {
	Matched        bool                 `json:"matched"`
	Source         string               `json:"source"`
	GroupID        int64                `json:"group_id"`
	PublicModel    string               `json:"public_model"`
	TargetPlatform string               `json:"target_platform"`
	UpstreamModel  string               `json:"upstream_model"`
	Endpoint       string               `json:"endpoint"`
	PriceModel     string               `json:"price_model,omitempty"`
	PriceSource    string               `json:"price_source,omitempty"`
	PriceAvailable bool                 `json:"price_available"`
	Route          *CompositeModelRoute `json:"route,omitempty"`
	Reason         string               `json:"reason,omitempty"`
	CandidateWins  bool                 `json:"candidate_wins,omitempty"`
}

type CompositeRouteInput struct {
	PublicModel    string `json:"public_model"`
	MatchType      string `json:"match_type"`
	TargetPlatform string `json:"target_platform"`
	UpstreamModel  string `json:"upstream_model"`
	Endpoint       string `json:"endpoint"`
	Priority       int    `json:"priority"`
	Enabled        bool   `json:"enabled"`
	Notes          string `json:"notes"`
}

type CompositeModelRouteRepository interface {
	ListByGroup(ctx context.Context, groupID int64, includeDisabled bool) ([]CompositeModelRoute, error)
	Create(ctx context.Context, route *CompositeModelRoute) error
	Update(ctx context.Context, route *CompositeModelRoute) error
	Delete(ctx context.Context, id int64) error
	DeleteByGroup(ctx context.Context, groupID int64) error
}

func normalizeCompositeRouteEndpoint(endpoint string) string {
	switch endpoint = strings.ToLower(strings.TrimSpace(endpoint)); endpoint {
	case CompositeRouteEndpointMessages, CompositeRouteEndpointCountTokens, CompositeRouteEndpointResponses,
		CompositeRouteEndpointChatCompletions, CompositeRouteEndpointEmbeddings, CompositeRouteEndpointImages,
		CompositeRouteEndpointGemini:
		return endpoint
	default:
		return CompositeRouteEndpointAny
	}
}

func normalizeCompositeRouteMatchType(matchType string) string {
	if strings.EqualFold(strings.TrimSpace(matchType), CompositeRouteMatchPrefix) {
		return CompositeRouteMatchPrefix
	}
	return CompositeRouteMatchExact
}

func normalizeCompositeRouteInput(input CompositeRouteInput) CompositeRouteInput {
	input.PublicModel = strings.TrimSpace(input.PublicModel)
	input.MatchType = normalizeCompositeRouteMatchType(input.MatchType)
	input.TargetPlatform = strings.ToLower(strings.TrimSpace(input.TargetPlatform))
	input.UpstreamModel = strings.TrimSpace(input.UpstreamModel)
	input.Endpoint = normalizeCompositeRouteEndpoint(input.Endpoint)
	// Prefix routes intentionally keep an empty upstream model so the concrete
	// requested suffix is passed through instead of collapsing to public_model.
	if input.UpstreamModel == "" && input.MatchType == CompositeRouteMatchExact {
		input.UpstreamModel = input.PublicModel
	}
	input.Notes = strings.TrimSpace(input.Notes)
	return input
}
