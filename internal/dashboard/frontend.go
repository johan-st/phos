package dashboard

import (
	"crypto/sha256"
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

//go:embed assets/*
var frontendAssets embed.FS

func assetURL(name string) string {
	data, _ := frontendAssets.ReadFile("assets/" + name)
	return fmt.Sprintf("/dashboard/assets/%s?v=%x", name, sha256.Sum256(data))
}

func (a *App) renderShell(query string) (string, error) {
	if _, err := parseSearchQuery(query, time.Now()); err != nil {
		return "", err
	}
	payload, _ := json.Marshal(map[string]any{"source": a.sourceID, "schema_version": 3})
	// encoding/json escapes HTML characters, including script closing tags.
	return fmt.Sprintf(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Phos dashboard</title><link rel="stylesheet" href="%s"></head>
<body class="app-dashboard"><div id="app"></div>
<noscript>The trace dashboard requires JavaScript.</noscript>
<script id="dashboard-bootstrap" type="application/json">%s</script>
<script type="module" src="%s"></script></body></html>`, assetURL("dashboard.css"), payload, assetURL("dashboard.js")), nil
}

func assetResponse(req requestMessage) responseMessage {
	name := strings.TrimPrefix(req.Path, "/dashboard/assets/")
	contentType := ""
	switch name {
	case "dashboard.js":
		contentType = "text/javascript; charset=utf-8"
	case "dashboard.css":
		contentType = "text/css; charset=utf-8"
	default:
		return errorResponse(req.ID, http.StatusNotFound, "dashboard asset not found")
	}
	body, err := frontendAssets.ReadFile("assets/" + name)
	if err != nil {
		return errorResponse(req.ID, http.StatusInternalServerError, "dashboard asset unavailable")
	}
	resp := textResponse(req.ID, http.StatusOK, contentType, string(body))
	resp.Headers["cache-control"] = "no-cache"
	resp.Headers["x-content-type-options"] = "nosniff"
	return resp
}
