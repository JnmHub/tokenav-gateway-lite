package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func (s *adminServiceImpl) ListCompositeRoutes(ctx context.Context, groupID int64) ([]CompositeModelRoute, error) {
	if err := s.requireCompositeGroup(ctx, groupID); err != nil {
		return nil, err
	}
	if s.compositeRouteRepo == nil {
		return nil, fmt.Errorf("composite route repository is not configured")
	}
	return s.compositeRouteRepo.ListByGroup(ctx, groupID, true)
}

func (s *adminServiceImpl) CreateCompositeRoute(ctx context.Context, groupID int64, input CompositeRouteInput) (*CompositeModelRoute, error) {
	if !s.isCompositeGroupsEnabled() {
		return nil, ErrCompositeGroupsDisabled
	}
	if err := s.requireCompositeGroup(ctx, groupID); err != nil {
		return nil, err
	}
	route, err := compositeRouteFromInput(groupID, input)
	if err != nil {
		return nil, err
	}
	if err := s.compositeRouteRepo.Create(ctx, route); err != nil {
		return nil, err
	}
	if err := s.refreshCompositeRoutes(ctx, groupID); err != nil {
		return nil, err
	}
	return route, nil
}

func (s *adminServiceImpl) UpdateCompositeRoute(ctx context.Context, groupID, routeID int64, input CompositeRouteInput) (*CompositeModelRoute, error) {
	if input.Enabled && !s.isCompositeGroupsEnabled() {
		return nil, ErrCompositeGroupsDisabled
	}
	if err := s.requireCompositeGroup(ctx, groupID); err != nil {
		return nil, err
	}
	if ok, err := s.compositeRouteBelongsToGroup(ctx, groupID, routeID); err != nil {
		return nil, err
	} else if !ok {
		return nil, ErrCompositeRouteNotFound
	}
	route, err := compositeRouteFromInput(groupID, input)
	if err != nil {
		return nil, err
	}
	route.ID = routeID
	if err := s.compositeRouteRepo.Update(ctx, route); err != nil {
		return nil, err
	}
	if err := s.refreshCompositeRoutes(ctx, groupID); err != nil {
		return nil, err
	}
	return route, nil
}

func (s *adminServiceImpl) DeleteCompositeRoute(ctx context.Context, groupID, routeID int64) error {
	if err := s.requireCompositeGroup(ctx, groupID); err != nil {
		return err
	}
	if ok, err := s.compositeRouteBelongsToGroup(ctx, groupID, routeID); err != nil {
		return err
	} else if !ok {
		return ErrCompositeRouteNotFound
	}
	if err := s.compositeRouteRepo.Delete(ctx, routeID); err != nil {
		return err
	}
	return s.refreshCompositeRoutes(ctx, groupID)
}

func (s *adminServiceImpl) BatchSetCompositeRoutesEnabled(ctx context.Context, groupID int64, routeIDs []int64, enabled bool) error {
	if enabled && !s.isCompositeGroupsEnabled() {
		return ErrCompositeGroupsDisabled
	}
	if err := s.requireCompositeGroup(ctx, groupID); err != nil {
		return err
	}
	if len(routeIDs) == 0 {
		return infraerrors.New(http.StatusBadRequest, "COMPOSITE_ROUTE_IDS_REQUIRED", "route_ids is required")
	}
	routes, err := s.compositeRouteRepo.ListByGroup(ctx, groupID, true)
	if err != nil {
		return err
	}
	byID := make(map[int64]CompositeModelRoute, len(routes))
	for _, route := range routes {
		byID[route.ID] = route
	}
	selected := make([]CompositeModelRoute, 0, len(routeIDs))
	seen := make(map[int64]struct{}, len(routeIDs))
	for _, id := range routeIDs {
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		route, ok := byID[id]
		if !ok {
			return ErrCompositeRouteNotFound
		}
		seen[id] = struct{}{}
		route.Enabled = enabled
		selected = append(selected, route)
	}

	opCtx := ctx
	var tx *dbent.Tx
	if s.entClient != nil {
		tx, err = s.entClient.Tx(ctx)
		if err != nil {
			return fmt.Errorf("begin composite route transaction: %w", err)
		}
		opCtx = dbent.NewTxContext(ctx, tx)
	}
	for i := range selected {
		if err := s.compositeRouteRepo.Update(opCtx, &selected[i]); err != nil {
			if tx != nil {
				_ = tx.Rollback()
			}
			return err
		}
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit composite route transaction: %w", err)
		}
	}
	return s.refreshCompositeRoutes(ctx, groupID)
}

func (s *adminServiceImpl) PreviewCompositeRoute(ctx context.Context, groupID int64, input CompositeRoutePreviewRequest) (*CompositeRouteDecision, error) {
	if err := s.requireCompositeGroup(ctx, groupID); err != nil {
		return nil, err
	}
	resolver := s.compositeResolver
	if resolver == nil {
		resolver = NewCompositeRouteResolver(s.compositeRouteRepo)
	}
	if input.Candidate != nil {
		candidate, err := compositeRouteFromInput(groupID, *input.Candidate)
		if err != nil {
			return nil, err
		}
		routes, err := s.compositeRouteRepo.ListByGroup(ctx, groupID, true)
		if err != nil {
			return nil, err
		}
		filtered := routes[:0]
		for _, route := range routes {
			if input.EditingRouteID <= 0 || route.ID != input.EditingRouteID {
				filtered = append(filtered, route)
			}
		}
		candidate.ID = input.EditingRouteID
		if candidate.ID <= 0 {
			// 新规则在完全相同的优先级下排在已有规则之后，预览不会虚假宣称它能胜出。
			candidate.ID = int64(^uint32(0) >> 1)
		}
		filtered = append(filtered, *candidate)
		model := strings.TrimSpace(input.Model)
		endpoint := normalizeCompositeRouteEndpoint(input.Endpoint)
		if route, ok := matchCompositeRoute(filtered, model, endpoint); ok {
			upstream := strings.TrimSpace(route.UpstreamModel)
			if upstream == "" {
				upstream = model
			}
			decision := &CompositeRouteDecision{Matched: true, Source: CompositeRouteSourceExplicit, GroupID: groupID,
				PublicModel: model, TargetPlatform: route.TargetPlatform, UpstreamModel: upstream,
				Endpoint: endpoint, Route: &route, CandidateWins: route.ID == candidate.ID,
				Reason: map[bool]string{true: "candidate_wins", false: "candidate_shadowed"}[route.ID == candidate.ID]}
			s.attachCompositePreviewPricing(ctx, groupID, decision)
			return decision, nil
		}
		if platform, ok := DetectModelPlatform(model); ok {
			decision := &CompositeRouteDecision{Matched: true, Source: CompositeRouteSourceDetector, GroupID: groupID,
				PublicModel: model, TargetPlatform: platform, UpstreamModel: model, Endpoint: endpoint}
			s.attachCompositePreviewPricing(ctx, groupID, decision)
			return decision, nil
		}
		return &CompositeRouteDecision{GroupID: groupID, PublicModel: model, Endpoint: endpoint,
			Reason: "no explicit route or built-in detector match"}, nil
	}
	decision, err := resolver.Resolve(ctx, groupID, input.Model, input.Endpoint)
	if err != nil {
		return nil, err
	}
	s.attachCompositePreviewPricing(ctx, groupID, &decision)
	return &decision, nil
}

func (s *adminServiceImpl) attachCompositePreviewPricing(ctx context.Context, groupID int64, decision *CompositeRouteDecision) {
	if decision == nil || !decision.Matched {
		return
	}
	decision.PriceModel = strings.TrimSpace(decision.UpstreamModel)
	if decision.PriceModel == "" {
		decision.PriceModel = strings.TrimSpace(decision.PublicModel)
	}
	if s.modelPricingResolver == nil || decision.PriceModel == "" {
		decision.PriceSource = "unavailable"
		return
	}
	priceCtx := WithResolvedTargetPlatform(ctx, decision.TargetPlatform)
	resolved := s.modelPricingResolver.Resolve(priceCtx, PricingInput{Model: decision.PriceModel, GroupID: &groupID})
	if resolved == nil || (resolved.BasePricing == nil && resolved.channelPricing == nil) {
		decision.PriceSource = "unavailable"
		return
	}
	decision.PriceSource = strings.TrimSpace(resolved.Source)
	decision.PriceAvailable = decision.PriceSource != ""
}

func (s *adminServiceImpl) requireCompositeGroup(ctx context.Context, groupID int64) error {
	group, err := s.groupRepo.GetByIDLite(ctx, groupID)
	if err != nil {
		return err
	}
	if group.Platform != PlatformComposite {
		return infraerrors.New(http.StatusBadRequest, "GROUP_NOT_COMPOSITE", "group is not a composite group")
	}
	if s.compositeRouteRepo == nil {
		return fmt.Errorf("composite route repository is not configured")
	}
	return nil
}

func (s *adminServiceImpl) compositeRouteBelongsToGroup(ctx context.Context, groupID, routeID int64) (bool, error) {
	routes, err := s.compositeRouteRepo.ListByGroup(ctx, groupID, true)
	if err != nil {
		return false, err
	}
	for i := range routes {
		if routes[i].ID == routeID {
			return true, nil
		}
	}
	return false, nil
}

func (s *adminServiceImpl) refreshCompositeRoutes(ctx context.Context, groupID int64) error {
	if s.compositeResolver == nil {
		return nil
	}
	if err := s.compositeResolver.RefreshGroup(ctx, groupID); err != nil {
		s.compositeResolver.InvalidateGroup(groupID)
		return fmt.Errorf("refresh composite route snapshot: %w", err)
	}
	return nil
}

func compositeRouteFromInput(groupID int64, input CompositeRouteInput) (*CompositeModelRoute, error) {
	input = normalizeCompositeRouteInput(input)
	if input.PublicModel == "" {
		return nil, infraerrors.New(http.StatusBadRequest, "PUBLIC_MODEL_REQUIRED", "public_model is required")
	}
	if !isConcreteRequestPlatform(input.TargetPlatform) {
		return nil, infraerrors.New(http.StatusBadRequest, "INVALID_TARGET_PLATFORM", "target_platform must be a concrete provider")
	}
	if input.Priority == 0 {
		input.Priority = 100
	}
	if input.Priority < -100000 || input.Priority > 100000 {
		return nil, infraerrors.New(http.StatusBadRequest, "INVALID_ROUTE_PRIORITY", "priority must be between -100000 and 100000")
	}
	return &CompositeModelRoute{GroupID: groupID, PublicModel: input.PublicModel, MatchType: input.MatchType,
		TargetPlatform: input.TargetPlatform, UpstreamModel: input.UpstreamModel, Endpoint: input.Endpoint,
		Priority: input.Priority, Enabled: input.Enabled, Notes: input.Notes}, nil
}
