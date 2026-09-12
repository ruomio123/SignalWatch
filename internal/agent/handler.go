package agent

import (
	"errors"
	"github.com/gin-gonic/gin"
	"signalwatch/internal/platform/httpx"
	"signalwatch/internal/subscription"
	"strconv"
	"strings"
)

type Handler struct{ Service *Service }

func (h Handler) Handle(c *gin.Context) {
	if h.Service == nil {
		httpx.WriteError(c, 503, "AI_DISABLED", "AI is disabled")
		return
	}
	uid, ok := httpx.CurrentUserID(c)
	if !ok || uid == 0 {
		httpx.WriteError(c, 401, "UNAUTHORIZED", "authentication required")
		return
	}
	s := h.Service
	ctx := c.Request.Context()
	u := uint64(uid)
	id := c.Param("id")
	path := c.FullPath()
	method := c.Request.Method
	switch {
	case strings.HasSuffix(path, "/conversations"):
		if method == "POST" {
			var input struct {
				Kind    string  `json:"kind"`
				PaperID *uint64 `json:"paper_id"`
			}
			if !httpx.BindJSON(c, &input) {
				return
			}
			v, err := s.CreateConversation(ctx, u, input.Kind, input.PaperID)
			if err != nil {
				writeError(c, err)
				return
			}
			c.JSON(201, v)
			return
		}
		page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
		if err != nil {
			writeError(c, ErrInput)
			return
		}
		var pid *uint64
		if raw := c.Query("paper_id"); raw != "" {
			v, e := strconv.ParseUint(raw, 10, 64)
			if e != nil || v == 0 {
				writeError(c, ErrInput)
				return
			}
			pid = &v
		}
		rows, err := s.Conversations(ctx, u, c.Query("kind"), pid, page)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(200, gin.H{"items": rows, "page": page})
	case strings.Contains(path, "/conversations/"):
		conversation, err := s.Conversation(ctx, u, id)
		if err != nil {
			writeError(c, err)
			return
		}
		if strings.HasSuffix(path, "/messages") {
			if method == "POST" {
				var input SubmitInput
				if !httpx.BindJSON(c, &input) {
					return
				}
				r, err := s.Submit(ctx, u, id, input)
				if err != nil {
					writeError(c, err)
					return
				}
				c.JSON(202, gin.H{"run_id": r.ID, "run": r})
				return
			}
			before, err := strconv.ParseUint(c.DefaultQuery("before", "0"), 10, 64)
			if err != nil {
				writeError(c, ErrInput)
				return
			}
			rows, err := s.Store.Messages(ctx, u, id, before)
			if err != nil {
				writeError(c, err)
				return
			}
			next := uint64(0)
			if len(rows) == 50 {
				next = rows[0].ID
			}
			c.JSON(200, gin.H{"items": rows, "next_before": next})
			return
		}
		if method == "DELETE" {
			if err := s.Store.DeleteConversation(ctx, u, id); err != nil {
				writeError(c, err)
				return
			}
			c.Status(204)
			return
		}
		c.JSON(200, conversation)
	case strings.Contains(path, "/runs/"):
		r, err := s.RunByID(ctx, u, id)
		if err != nil {
			writeError(c, err)
			return
		}
		if method == "POST" {
			if err := s.Store.Cancel(ctx, u, id); err != nil {
				writeError(c, err)
				return
			}
			c.Status(204)
			return
		}
		steps, err := s.Store.Steps(ctx, u, id)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(200, gin.H{"run": r, "steps": steps})
	case strings.Contains(path, "/subscription-drafts/"):
		d, err := s.Store.Draft(ctx, u, id)
		if err != nil {
			writeError(c, err)
			return
		}
		if _, err := s.Conversation(ctx, u, d.ConversationID); err != nil {
			writeError(c, err)
			return
		}
		if method == "GET" {
			c.JSON(200, d)
			return
		}
		if method == "PATCH" {
			var input struct {
				Version uint32                   `json:"version"`
				Payload subscription.CreateInput `json:"payload"`
			}
			if !httpx.BindJSON(c, &input) {
				return
			}
			normalized, err := s.Subscriptions.ValidateDraft(ctx, u, input.Payload)
			if err != nil {
				writeError(c, ErrInput)
				return
			}
			value, err := s.Store.EditDraft(ctx, u, id, input.Version, normalized)
			if err != nil {
				writeError(c, err)
				return
			}
			c.JSON(200, value)
			return
		}
		var input struct {
			Version uint32 `json:"version"`
		}
		if !httpx.BindJSON(c, &input) {
			return
		}
		result, err := s.Confirm(ctx, u, id, input.Version)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(200, result)
	default:
		writeError(c, ErrNotFound)
	}
}
func writeError(c *gin.Context, err error) {
	status, code := 503, "AGENT_UNAVAILABLE"
	var model *ModelError
	switch {
	case errors.As(err, &model):
		code = model.Code
		status = 422
		if code == "AI_CONFIGURATION_REQUIRED" {
			status = 409
		}
	case errors.Is(err, ErrInput):
		status = 400
		code = "AGENT_INVALID_INPUT"
	case errors.Is(err, ErrNotFound), errors.Is(err, subscription.ErrNotFound):
		status = 404
		code = "AGENT_NOT_FOUND"
	case errors.Is(err, ErrConflict), errors.Is(err, subscription.ErrDraftConflict):
		status = 409
		code = "AGENT_CONFLICT"
	case errors.Is(err, subscription.ErrLimitReached):
		status = 409
		code = "SUBSCRIPTION_LIMIT_REACHED"
	case errors.Is(err, subscription.ErrAIConfigurationRequired):
		status = 409
		code = "AI_CONFIGURATION_REQUIRED"
	}
	httpx.WriteError(c, status, code, code)
}
