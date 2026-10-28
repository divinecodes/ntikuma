package platform

import "os"

// ActivationToken returns the xdg-activation token the launcher gave this
// process, if any. GNOME passes it as XDG_ACTIVATION_TOKEN on Wayland and as
// DESKTOP_STARTUP_ID on X11. The second instance (`ntikuma toggle`) forwards it
// to the running app so the popup can take focus.
func ActivationToken() string {
	if t := os.Getenv("XDG_ACTIVATION_TOKEN"); t != "" {
		return t
	}

	return os.Getenv("DESKTOP_STARTUP_ID")
}
