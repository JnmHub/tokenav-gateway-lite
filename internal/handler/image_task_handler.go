package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type ImageTaskHandler struct {
	service *service.ImageTaskService
}

type imageTaskArtifactDTO struct {
	FileName    string `json:"file_name"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
	DownloadURL string `json:"download_url,omitempty"`
}

type imageTaskDTO struct {
	ID              string                 `json:"id"`
	UserID          int64                  `json:"user_id,omitempty"`
	State           string                 `json:"state"`
	SettlementState string                 `json:"settlement_state"`
	Progress        int                    `json:"progress"`
	Model           string                 `json:"model"`
	ImageCount      int                    `json:"image_count"`
	Attempt         int                    `json:"attempt"`
	MaxAttempts     int                    `json:"max_attempts"`
	ReservedAmount  float64                `json:"reserved_amount"`
	ActualAmount    float64                `json:"actual_amount"`
	CancelRequested bool                   `json:"cancel_requested"`
	ErrorCode       string                 `json:"error_code,omitempty"`
	ErrorMessage    string                 `json:"error_message,omitempty"`
	Artifacts       []imageTaskArtifactDTO `json:"artifacts"`
	CreatedAt       time.Time              `json:"created_at"`
	UpdatedAt       time.Time              `json:"updated_at"`
	StartedAt       *time.Time             `json:"started_at,omitempty"`
	CompletedAt     *time.Time             `json:"completed_at,omitempty"`
	ExpiresAt       time.Time              `json:"expires_at"`
}

func ProvideImageTaskHandler(
	openAI *OpenAIGatewayHandler,
	accounts service.AccountRepository,
	apiKeys *service.APIKeyService,
	store service.ImageTaskStore,
	billing service.ImageTaskBilling,
	objects service.ImageTaskObjectStore,
	encryptor service.SecretEncryptor,
	cfg service.ImageTaskConfig,
) (*ImageTaskHandler, error) {
	executor := NewGatewayImageTaskExecutor(openAI, accounts, apiKeys, cfg)
	imageTasks, err := service.NewImageTaskService(store, billing, objects, executor, encryptor, cfg)
	if err != nil {
		return nil, err
	}
	if cfg.Enabled {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = store.Ready(ctx)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("image task queue is not ready: %w", err)
		}
	}
	openAI.SetImageTaskService(imageTasks)
	imageTasks.Start()
	return &ImageTaskHandler{service: imageTasks}, nil
}

func (h *ImageTaskHandler) Stop() {
	if h != nil && h.service != nil {
		h.service.Stop()
	}
}

func (h *ImageTaskHandler) RuntimeEnabled() bool {
	return h != nil && h.service != nil && h.service.Config().Enabled
}

// ActiveTaskCount is used by the local configuration page to prevent turning
// workers off while durable quota reservations or running work still exist.
// It returns aggregate counts only and exposes no task or user data.
func (h *ImageTaskHandler) ActiveTaskCount(ctx context.Context) (int64, error) {
	if h == nil || h.service == nil {
		return 0, nil
	}
	metrics, err := h.service.Metrics(ctx)
	if err != nil {
		return 0, err
	}
	return metrics["queue_depth"], nil
}

func (h *OpenAIGatewayHandler) shouldSubmitImageTaskAsync(c *gin.Context) bool {
	if c == nil {
		return false
	}
	for _, preference := range strings.Split(c.GetHeader("Prefer"), ",") {
		if strings.EqualFold(strings.TrimSpace(strings.SplitN(preference, ";", 2)[0]), "respond-async") {
			return true
		}
	}
	return false
}

func (h *OpenAIGatewayHandler) submitImageTaskAsync(
	c *gin.Context,
	apiKey *service.APIKey,
	subject servermiddleware.AuthSubject,
	parsed *service.OpenAIImagesRequest,
	body []byte,
	requestModel string,
	channelMapping service.ChannelMappingResult,
	subscription *service.UserSubscription,
) bool {
	if !h.shouldSubmitImageTaskAsync(c) {
		return false
	}
	if h.imageTaskService == nil || !h.imageTaskService.Config().Enabled {
		h.errorResponse(c, http.StatusServiceUnavailable, "async_image_tasks_disabled", "Asynchronous image tasks are not enabled on this gateway")
		return true
	}
	if parsed == nil || parsed.Multipart || parsed.Stream || !strings.HasSuffix(strings.TrimRight(c.Request.URL.Path, "/"), "/images/generations") {
		h.errorResponse(c, http.StatusUnprocessableEntity, "async_image_request_unsupported", "Asynchronous image tasks currently support non-streaming JSON image generations only")
		return true
	}
	imageCount := parsed.N
	if imageCount <= 0 {
		imageCount = 1
	}
	billingModel := requestModel
	switch channelMapping.BillingModelSource {
	case service.BillingModelSourceUpstream:
		h.errorResponse(c, http.StatusUnprocessableEntity, "async_image_price_unbounded", "Asynchronous image tasks require a price that can be frozen before execution")
		return true
	case service.BillingModelSourceChannelMapped:
		if channelMapping.MappedModel != "" {
			billingModel = channelMapping.MappedModel
		}
	case service.BillingModelSourceRequested:
		billingModel = requestModel
	default:
		if channelMapping.Mapped && channelMapping.MappedModel != "" {
			billingModel = channelMapping.MappedModel
		}
	}
	billingSnapshot, maxReservedCents, err := h.gatewayService.PrepareImageTaskBilling(c.Request.Context(), apiKey, billingModel, parsed.SizeTier, imageCount)
	if err != nil {
		h.errorResponse(c, http.StatusUnprocessableEntity, "async_image_price_unavailable", err.Error())
		return true
	}
	requestHash := service.HashUsageRequestPayload(body)
	policy, err := service.CaptureImageTaskPolicy(
		c.Request.Context(), apiKey, subscription,
		clientRequestedUsageFields(c, channelMapping, requestModel, channelMapping.MappedModel),
		c.GetHeader("User-Agent"), ip.GetClientIP(c), service.ExtractClientSessionID(c), requestHash,
	)
	if err != nil {
		h.errorResponse(c, http.StatusUnprocessableEntity, "async_image_policy_unavailable", "Image task policy could not be frozen")
		return true
	}
	// Full sub2api mode has no regional lease reservation. Until it has a
	// durable wallet hold primitive, accepting delayed paid work there would
	// allow the balance to be spent twice. Gateway-local keys are exempt.
	if strings.TrimSpace(policy.GatewayKeyID) == "" && !policy.BillingExempt {
		h.errorResponse(c, http.StatusConflict, "async_image_requires_gateway_lite", "Paid asynchronous image tasks require gateway-lite quota leasing")
		return true
	}
	policyPayload, err := policy.Marshal()
	if err != nil {
		h.errorResponse(c, http.StatusUnprocessableEntity, "async_image_policy_unavailable", "Image task policy could not be frozen")
		return true
	}
	billingPayload, err := json.Marshal(billingSnapshot)
	if err != nil {
		h.errorResponse(c, http.StatusInternalServerError, "async_image_price_unavailable", "Image task price could not be frozen")
		return true
	}
	idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if len(idempotencyKey) > 128 {
		h.errorResponse(c, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key must not exceed 128 characters")
		return true
	}
	ctx := gatewaylite.ContextWithAsyncSettlement(c.Request.Context())
	c.Request = c.Request.WithContext(ctx)
	task, replayed, err := h.imageTaskService.Submit(ctx, service.ImageTaskCreateInput{
		IdempotencyKey: idempotencyKey, UserID: subject.UserID, APIKeyID: apiKey.ID,
		GatewayKeyID: policy.GatewayKeyID, GroupID: apiKey.GroupID, Model: requestModel,
		Endpoint: c.Request.URL.Path, ContentType: c.GetHeader("Content-Type"), ImageCount: imageCount,
		RequestPayload: body, PolicySnapshot: policyPayload, BillingSnapshot: billingPayload,
		MaxReservedCents: maxReservedCents,
	})
	if err != nil {
		if errors.Is(err, service.ErrImageTaskPersistenceUncertain) {
			ctx = gatewaylite.ContextWithUncertainAsyncPersistence(ctx)
			c.Request = c.Request.WithContext(ctx)
		} else {
			gatewaylite.MarkRequestReservationForRefund(ctx)
		}
		status, code := imageTaskHTTPError(err)
		h.errorResponse(c, status, code, imageTaskPublicError(err))
		return true
	}
	c.Header("Preference-Applied", "respond-async")
	c.Header("Location", "/v1/images/tasks/"+task.ID)
	c.Header("Retry-After", "2")
	dto, _ := imageTaskToDTO(c.Request.Context(), h.imageTaskService, task, "api")
	c.JSON(http.StatusAccepted, gin.H{"object": "image.task", "replayed": replayed, "task": dto})
	return true
}

func (h *ImageTaskHandler) APIList(c *gin.Context) {
	userID, apiKeyID, gatewayKeyID, ok := imageTaskAPIOwner(c)
	if !ok {
		return
	}
	items, next, err := h.service.ListOwned(c.Request.Context(), userID, apiKeyID, gatewayKeyID, imageTaskLimit(c), c.Query("cursor"))
	h.writeList(c, items, next, "api", err)
}

func (h *ImageTaskHandler) APIGet(c *gin.Context) {
	userID, apiKeyID, gatewayKeyID, ok := imageTaskAPIOwner(c)
	if !ok {
		return
	}
	task, err := h.service.GetOwned(c.Request.Context(), c.Param("id"), userID, apiKeyID, gatewayKeyID)
	h.writeOne(c, task, "api", err)
}

func (h *ImageTaskHandler) APICancel(c *gin.Context) {
	userID, apiKeyID, gatewayKeyID, ok := imageTaskAPIOwner(c)
	if !ok {
		return
	}
	task, err := h.service.Cancel(c.Request.Context(), c.Param("id"), userID, apiKeyID, gatewayKeyID)
	h.writeOne(c, task, "api", err)
}

func (h *ImageTaskHandler) APIArtifact(c *gin.Context) {
	userID, apiKeyID, gatewayKeyID, ok := imageTaskAPIOwner(c)
	if !ok {
		return
	}
	h.streamArtifact(c, func(index int) (io.ReadCloser, service.ImageTaskArtifact, error) {
		return h.service.OpenOwnedArtifact(c.Request.Context(), c.Param("id"), index, userID, apiKeyID, gatewayKeyID)
	})
}

func (h *ImageTaskHandler) UserList(c *gin.Context) {
	subject, ok := servermiddleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	items, next, err := h.service.ListForUser(c.Request.Context(), subject.UserID, imageTaskLimit(c), c.Query("cursor"))
	h.writeList(c, items, next, "user", err)
}

func (h *ImageTaskHandler) UserGet(c *gin.Context) {
	subject, ok := servermiddleware.GetAuthSubjectFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	task, err := h.service.GetForUser(c.Request.Context(), c.Param("id"), subject.UserID)
	h.writeOne(c, task, "user", err)
}

func (h *ImageTaskHandler) UserCancel(c *gin.Context) {
	subject, ok := servermiddleware.GetAuthSubjectFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	task, err := h.service.CancelForUser(c.Request.Context(), c.Param("id"), subject.UserID)
	h.writeOne(c, task, "user", err)
}

func (h *ImageTaskHandler) UserArtifact(c *gin.Context) {
	subject, ok := servermiddleware.GetAuthSubjectFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	h.streamArtifact(c, func(index int) (io.ReadCloser, service.ImageTaskArtifact, error) {
		return h.service.OpenUserArtifact(c.Request.Context(), c.Param("id"), index, subject.UserID)
	})
}

func (h *ImageTaskHandler) AdminList(c *gin.Context) {
	if imageTaskUsesGatewayAdminKey(c) || strings.TrimSpace(c.Query("user_id")) != "" {
		userID, ok := imageTaskAdminUserID(c)
		if !ok {
			return
		}
		items, next, err := h.service.ListForAdminUser(c.Request.Context(), userID, c.Query("state"), imageTaskLimit(c), c.Query("cursor"))
		h.writeList(c, items, next, "admin", err)
		return
	}
	items, next, err := h.service.ListForAdmin(c.Request.Context(), c.Query("state"), imageTaskLimit(c), c.Query("cursor"))
	h.writeList(c, items, next, "admin", err)
}

func (h *ImageTaskHandler) AdminGet(c *gin.Context) {
	if imageTaskUsesGatewayAdminKey(c) || strings.TrimSpace(c.Query("user_id")) != "" {
		userID, ok := imageTaskAdminUserID(c)
		if !ok {
			return
		}
		task, err := h.service.GetForAdminUser(c.Request.Context(), c.Param("id"), userID)
		h.writeOne(c, task, "admin", err)
		return
	}
	task, err := h.service.GetForAdmin(c.Request.Context(), c.Param("id"))
	h.writeOne(c, task, "admin", err)
}

func (h *ImageTaskHandler) AdminReplay(c *gin.Context) {
	servermiddleware.SetAuditAction(c, "admin.image_tasks.replay")
	servermiddleware.SetAuditResource(c, "image_task", c.Param("id"))
	if !imageTaskRequireConfirmation(c, "REPLAY_IMAGE_TASK") {
		return
	}
	if imageTaskUsesGatewayAdminKey(c) || strings.TrimSpace(c.Query("user_id")) != "" {
		userID, ok := imageTaskAdminUserID(c)
		if !ok {
			return
		}
		servermiddleware.SetAuditSummary(c, map[string]any{"user_id": userID})
		task, err := h.service.ReplayForUser(c.Request.Context(), c.Param("id"), userID)
		h.writeOne(c, task, "admin", err)
		return
	}
	task, err := h.service.Replay(c.Request.Context(), c.Param("id"))
	h.writeOne(c, task, "admin", err)
}

func (h *ImageTaskHandler) AdminCleanup(c *gin.Context) {
	servermiddleware.SetAuditAction(c, "admin.image_tasks.cleanup")
	servermiddleware.SetAuditResource(c, "image_task", "expired")
	if !imageTaskRequireConfirmation(c, "CLEANUP_IMAGE_TASKS") {
		return
	}
	servermiddleware.SetAuditSummary(c, map[string]any{"limit": imageTaskLimit(c)})
	removed, err := h.service.Cleanup(c.Request.Context(), imageTaskLimit(c))
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"removed": removed})
}

func (h *ImageTaskHandler) AdminCancel(c *gin.Context) {
	servermiddleware.SetAuditAction(c, "admin.image_tasks.cancel")
	servermiddleware.SetAuditResource(c, "image_task", c.Param("id"))
	if imageTaskUsesGatewayAdminKey(c) || strings.TrimSpace(c.Query("user_id")) != "" {
		userID, ok := imageTaskAdminUserID(c)
		if !ok {
			return
		}
		servermiddleware.SetAuditSummary(c, map[string]any{"user_id": userID})
		task, err := h.service.CancelForUser(c.Request.Context(), c.Param("id"), userID)
		h.writeOne(c, task, "admin", err)
		return
	}
	owned, err := h.service.GetForAdmin(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.writeError(c, err)
		return
	}
	task, err := h.service.Cancel(c.Request.Context(), owned.ID, owned.UserID, owned.APIKeyID, owned.GatewayKeyID)
	h.writeOne(c, task, "admin", err)
}

func (h *ImageTaskHandler) AdminArtifact(c *gin.Context) {
	if imageTaskUsesGatewayAdminKey(c) || strings.TrimSpace(c.Query("user_id")) != "" {
		userID, ok := imageTaskAdminUserID(c)
		if !ok {
			return
		}
		h.streamArtifact(c, func(index int) (io.ReadCloser, service.ImageTaskArtifact, error) {
			return h.service.OpenUserArtifact(c.Request.Context(), c.Param("id"), index, userID)
		})
		return
	}
	h.streamArtifact(c, func(index int) (io.ReadCloser, service.ImageTaskArtifact, error) {
		task, err := h.service.GetForAdmin(c.Request.Context(), c.Param("id"))
		if err != nil {
			return nil, service.ImageTaskArtifact{}, err
		}
		return h.service.OpenOwnedArtifact(c.Request.Context(), task.ID, index, task.UserID, task.APIKeyID, task.GatewayKeyID)
	})
}

func (h *ImageTaskHandler) AdminMetrics(c *gin.Context) {
	metrics, err := h.service.Metrics(c.Request.Context())
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"metrics": metrics, "config": h.service.Config()})
}

func (h *ImageTaskHandler) AdminCapabilities(c *gin.Context) {
	enabled := h != nil && h.service != nil && h.service.Config().Enabled
	c.JSON(http.StatusOK, gin.H{"schema_version": 1, "enabled": enabled})
}

func (h *ImageTaskHandler) writeList(c *gin.Context, tasks []*service.ImageTask, next, view string, err error) {
	if err != nil {
		h.writeError(c, err)
		return
	}
	items := make([]imageTaskDTO, 0, len(tasks))
	for _, task := range tasks {
		dto, dtoErr := imageTaskToDTO(c.Request.Context(), h.service, task, view)
		if dtoErr == nil {
			items = append(items, dto)
		}
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "next_cursor": next})
}

func (h *ImageTaskHandler) writeOne(c *gin.Context, task *service.ImageTask, view string, err error) {
	if err != nil {
		h.writeError(c, err)
		return
	}
	dto, err := imageTaskToDTO(c.Request.Context(), h.service, task, view)
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"task": dto})
}

func (h *ImageTaskHandler) writeError(c *gin.Context, err error) {
	status, code := imageTaskHTTPError(err)
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": imageTaskPublicError(err)}})
}

func (h *ImageTaskHandler) streamArtifact(c *gin.Context, open func(int) (io.ReadCloser, service.ImageTaskArtifact, error)) {
	index, err := strconv.Atoi(c.Param("index"))
	if err != nil || index < 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "artifact not found"})
		return
	}
	body, artifact, err := open(index)
	if err != nil {
		h.writeError(c, err)
		return
	}
	defer body.Close()
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": artifact.FileName})
	c.Header("Content-Disposition", disposition)
	c.DataFromReader(http.StatusOK, artifact.SizeBytes, artifact.ContentType, body, nil)
}

func imageTaskAPIOwner(c *gin.Context) (int64, int64, string, bool) {
	apiKey, keyOK := servermiddleware.GetAPIKeyFromContext(c)
	subject, subjectOK := servermiddleware.GetAuthSubjectFromContext(c)
	if !keyOK || !subjectOK || apiKey == nil || subject.UserID <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return 0, 0, "", false
	}
	gatewayKeyID := ""
	if meta, ok := gatewaylite.RequestMetaFromContext(c.Request.Context()); ok {
		gatewayKeyID = meta.KeyID
	}
	return subject.UserID, apiKey.ID, gatewayKeyID, true
}

func imageTaskToDTO(ctx context.Context, tasks *service.ImageTaskService, task *service.ImageTask, view string) (imageTaskDTO, error) {
	if task == nil {
		return imageTaskDTO{}, service.ErrImageTaskNotFound
	}
	dto := imageTaskDTO{
		ID: task.ID, State: task.State, SettlementState: task.SettlementState, Progress: task.Progress,
		Model: task.Model, ImageCount: task.ImageCount, Attempt: task.Attempt, MaxAttempts: task.MaxAttempts,
		ReservedAmount: float64(task.ReservedCents) / 100, ActualAmount: float64(task.ActualCents) / 100,
		CancelRequested: task.CancelRequested, ErrorCode: task.ErrorCode, ErrorMessage: imageTaskErrorMessage(task.ErrorCode),
		Artifacts: make([]imageTaskArtifactDTO, 0, len(task.Artifacts)), CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt,
		StartedAt: task.StartedAt, CompletedAt: task.CompletedAt, ExpiresAt: task.ExpiresAt,
	}
	// 异步任务虽然通过 HTTP 200 查询状态，但失败文案仍属于客户可见错误。
	// 仅改写用户视图，后台排障继续保留真实原因和稳定的错误码。
	if view == "user" && dto.ErrorMessage != "" {
		dto.ErrorMessage = service.GatewayClientErrorMessage(ctx, dto.ErrorMessage)
	}
	if view == "admin" {
		dto.UserID = task.UserID
	}
	urls := []string(nil)
	if task.State == service.ImageTaskStateSucceeded && task.SettlementState == service.ImageTaskSettlementSettled {
		var err error
		urls, err = tasks.DownloadURLs(ctx, task)
		if err != nil {
			return imageTaskDTO{}, err
		}
	}
	for index, artifact := range task.Artifacts {
		downloadURL := ""
		if index < len(urls) {
			downloadURL = urls[index]
			if strings.HasPrefix(downloadURL, "/v1/images/tasks/") && view == "user" {
				downloadURL = fmt.Sprintf("/api/v1/image-tasks/%s/artifacts/%d", task.ID, index)
			}
			if view == "admin" && strings.HasPrefix(downloadURL, "/v1/images/tasks/") {
				downloadURL = ""
			}
		}
		dto.Artifacts = append(dto.Artifacts, imageTaskArtifactDTO{
			FileName: artifact.FileName, ContentType: artifact.ContentType, SizeBytes: artifact.SizeBytes,
			SHA256: artifact.SHA256, DownloadURL: downloadURL,
		})
	}
	return dto, nil
}

func imageTaskLimit(c *gin.Context) int {
	limit, _ := strconv.Atoi(c.Query("limit"))
	if limit <= 0 || limit > 200 {
		return 50
	}
	return limit
}

func imageTaskAdminUserID(c *gin.Context) (int64, bool) {
	value := strings.TrimSpace(c.Query("user_id"))
	userID, err := strconv.ParseInt(value, 10, 64)
	if err != nil || userID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"code": "invalid_user_id", "message": "user_id must be a positive integer",
		}})
		return 0, false
	}
	return userID, true
}

func imageTaskUsesGatewayAdminKey(c *gin.Context) bool {
	return c != nil && c.GetString("auth_method") == "gateway_lite_admin_key"
}

func imageTaskRequireConfirmation(c *gin.Context, expected string) bool {
	var input struct {
		Confirm string `json:"confirm" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || strings.TrimSpace(input.Confirm) != expected {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"code": "confirmation_required", "message": "explicit operation confirmation is required",
		}})
		return false
	}
	return true
}

func imageTaskHTTPError(err error) (int, string) {
	switch {
	case errors.Is(err, service.ErrImageTaskNotFound):
		return http.StatusNotFound, "image_task_not_found"
	case errors.Is(err, service.ErrImageTaskForbidden):
		return http.StatusForbidden, "image_task_forbidden"
	case errors.Is(err, service.ErrImageTaskConflict):
		return http.StatusConflict, "image_task_conflict"
	case errors.Is(err, service.ErrImageTaskAlreadyRunning):
		return http.StatusConflict, "image_task_already_running"
	case errors.Is(err, service.ErrImageTaskQueueFull):
		return http.StatusTooManyRequests, "image_task_queue_full"
	case errors.Is(err, service.ErrImageTaskStorageDisabled):
		return http.StatusServiceUnavailable, "image_tasks_disabled"
	case errors.Is(err, service.ErrImageTaskPersistenceUncertain):
		return http.StatusServiceUnavailable, "image_task_persistence_uncertain"
	case errors.Is(err, service.ErrImageTaskInvalidInput):
		return http.StatusBadRequest, "invalid_image_task_input"
	case errors.Is(err, gatewaylite.ErrRequestQuotaInsufficient):
		return http.StatusPaymentRequired, "insufficient_quota"
	case errors.Is(err, gatewaylite.ErrRequestQuotaUnavailable):
		return http.StatusServiceUnavailable, "quota_unavailable"
	default:
		return http.StatusInternalServerError, "image_task_error"
	}
}

func imageTaskPublicError(err error) string {
	status, code := imageTaskHTTPError(err)
	_ = status
	switch code {
	case "image_task_not_found":
		return "Image task not found"
	case "image_task_forbidden":
		return "Image task does not belong to this user or API key"
	case "image_task_conflict":
		return "Image task is not in a state that permits this operation"
	case "image_task_already_running":
		return "Only queued image tasks can be cancelled; this task has already been dispatched"
	case "image_task_queue_full":
		return "Image task queue or user concurrency limit is full"
	case "image_tasks_disabled":
		return "Asynchronous image tasks are unavailable"
	case "image_task_persistence_uncertain":
		return "The task submission outcome is uncertain; retry with the same idempotency key"
	case "insufficient_quota":
		return "Insufficient quota for this image task"
	case "quota_unavailable":
		return "Quota service is temporarily unavailable"
	case "invalid_image_task_input":
		return "Image task request parameters are invalid"
	default:
		return "Image task operation failed"
	}
}

func imageTaskErrorMessage(code string) string {
	switch strings.TrimSpace(code) {
	case "request_decrypt_failed":
		return "The saved request could not be restored"
	case "upstream_execution_failed":
		return "The upstream image service failed after retries"
	case "object_storage_failed":
		return "The generated image could not be saved"
	case "object_persist_failed":
		return "The generated image could not be saved and was not retried"
	case "execution_outcome_unknown":
		return "The image provider outcome could not be confirmed and was not retried"
	case "request_integrity_failed":
		return "The saved request failed its integrity check"
	case "settlement_retry", "release_retry":
		return "Billing confirmation is being retried"
	default:
		return ""
	}
}
