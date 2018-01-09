package dashboardScreenOWl

import (
	"net/http"

	"github.com/fwtpe/owl-backend/modules/f2e-api/app/utils"
	"github.com/fwtpe/owl-backend/modules/f2e-api/config"
	"github.com/gin-gonic/gin"
)

var db config.DBPool

const badstatus = http.StatusBadRequest
const expecstatus = http.StatusExpectationFailed

func Routes(r *gin.Engine) {
	db = config.Con()
	sa := r.Group("/api/v1/dashboard/screen_all")
	sa.Use(utils.AuthSessionMidd)
	sa.GET("", GetScreenList)
	expr := r.Group("/api/v1/dashboard/screen")
	expr.Use(utils.AuthSessionMidd)
	expr.POST("", CreateScreen)
	expr.GET("/:sid", GetScreen)
	expr.PUT("", UpdateScreen)
	expr.DELETE("/:sid", DeleteScreen)
}
