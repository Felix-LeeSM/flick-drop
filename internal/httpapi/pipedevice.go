package httpapi

import "strings"

// pipeDeviceLabel turns a User-Agent into a coarse label such as "Safari on
// iPhone" so the live relay sender can tell waiting receivers apart. It is a
// display hint, not an identity: any client can send any User-Agent. The raw
// User-Agent is never stored, returned, or logged; only this label is kept in
// memory for the session.
func pipeDeviceLabel(userAgent string) string {
	browser := pipeBrowserName(userAgent)
	platform := pipePlatformName(userAgent)
	switch {
	case browser != "" && platform != "":
		return browser + " on " + platform
	case browser != "":
		return browser
	case platform != "":
		return "Unknown browser on " + platform
	default:
		return "Unknown browser"
	}
}

// pipeBrowserName checks the more specific tokens first: Edge, Opera, and
// Samsung Internet all also claim "Chrome" and "Safari", and Chrome on iOS
// (CriOS) also claims "Safari".
func pipeBrowserName(ua string) string {
	switch {
	case strings.Contains(ua, "Edg/"), strings.Contains(ua, "EdgA/"), strings.Contains(ua, "EdgiOS/"):
		return "Edge"
	case strings.Contains(ua, "OPR/"), strings.Contains(ua, "Opera"):
		return "Opera"
	case strings.Contains(ua, "SamsungBrowser/"):
		return "Samsung Internet"
	case strings.Contains(ua, "Firefox/"), strings.Contains(ua, "FxiOS/"):
		return "Firefox"
	case strings.Contains(ua, "CriOS/"), strings.Contains(ua, "Chrome/"), strings.Contains(ua, "Chromium/"):
		return "Chrome"
	case strings.Contains(ua, "Safari/"):
		return "Safari"
	default:
		return ""
	}
}

// pipePlatformName checks iPhone/iPad before macOS (their User-Agents say
// "like Mac OS X") and Android before Linux.
func pipePlatformName(ua string) string {
	switch {
	case strings.Contains(ua, "iPhone"):
		return "iPhone"
	case strings.Contains(ua, "iPad"):
		return "iPad"
	case strings.Contains(ua, "Android"):
		return "Android"
	case strings.Contains(ua, "Windows"):
		return "Windows"
	case strings.Contains(ua, "CrOS"):
		return "ChromeOS"
	case strings.Contains(ua, "Macintosh"), strings.Contains(ua, "Mac OS X"):
		return "macOS"
	case strings.Contains(ua, "Linux"):
		return "Linux"
	default:
		return ""
	}
}
