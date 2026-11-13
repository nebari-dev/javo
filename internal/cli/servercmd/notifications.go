package servercmd

// notificationFlags are the controller's settings for failure notices.
type notificationFlags struct {
	namespace, configMap, secret string
}

// enabled: the chart passes these flags only when notifications.enabled is true.
func (f notificationFlags) enabled() bool { return f.namespace != "" }
