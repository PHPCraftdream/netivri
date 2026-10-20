package core

func copyAccountRoutes(routes map[string]string) map[string]string {
	if len(routes) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(routes))
	for login, routeKey := range routes {
		out[login] = routeKey
	}
	return out
}
