package api

import (
	"fmt"
	"net/http"
	"strings"

	"moesekai/server/internal/model"
)

func requestLocale(w http.ResponseWriter, r *http.Request, bodyLocale string) (string, bool, bool) {
	queryLocale := strings.TrimSpace(r.URL.Query().Get("locale"))
	bodyLocale = strings.TrimSpace(bodyLocale)
	locale := queryLocale
	if bodyLocale != "" {
		locale = bodyLocale
	}
	explicit := locale != ""
	if !explicit {
		return model.LocaleChinese, false, true
	}
	if !model.IsValidLocale(locale) {
		writeErr(w, http.StatusBadRequest, "unsupported locale")
		return "", true, false
	}
	return locale, true, true
}

func writeLocaleInternalError(w http.ResponseWriter, explicit bool, err error) {
	if explicit {
		writeContractError(w, http.StatusInternalServerError, "internal_error", nil, nil)
		return
	}
	writeErr(w, http.StatusInternalServerError, fmt.Sprint(err))
}
