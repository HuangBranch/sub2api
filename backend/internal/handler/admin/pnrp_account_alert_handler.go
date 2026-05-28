package admin

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type PNRPAccountAlertHandler struct {
	configService *service.PNRPAccountAlertConfigService
	notifier      *service.PNRPAccountFailureEmailNotifier
}

func NewPNRPAccountAlertHandler(configService *service.PNRPAccountAlertConfigService, notifier *service.PNRPAccountFailureEmailNotifier) *PNRPAccountAlertHandler {
	return &PNRPAccountAlertHandler{configService: configService, notifier: notifier}
}

// GetConfig returns the PNRP custom account alert configuration.
// GET /api/v1/admin/accounts/pnrp-alert-config
func (h *PNRPAccountAlertHandler) GetConfig(c *gin.Context) {
	if h == nil || h.configService == nil {
		response.Error(c, http.StatusServiceUnavailable, "PNRP account alert service not available")
		return
	}

	cfg, err := h.configService.GetConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}

// RunCheck runs one PNRP custom account alert check immediately.
// POST /api/v1/admin/accounts/pnrp-alert-check
func (h *PNRPAccountAlertHandler) RunCheck(c *gin.Context) {
	if h == nil || h.notifier == nil {
		response.Error(c, http.StatusServiceUnavailable, "PNRP account alert service not available")
		return
	}

	summary, err := h.notifier.RunAccountAlertCheck(c.Request.Context(), true)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, summary)
}

// UpdateConfig updates the PNRP custom account alert configuration.
// PUT /api/v1/admin/accounts/pnrp-alert-config
func (h *PNRPAccountAlertHandler) UpdateConfig(c *gin.Context) {
	if h == nil || h.configService == nil {
		response.Error(c, http.StatusServiceUnavailable, "PNRP account alert service not available")
		return
	}

	var req service.PNRPAccountAlertConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	cfg, err := h.configService.UpdateConfig(c.Request.Context(), req)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}
