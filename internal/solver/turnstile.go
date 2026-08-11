package solver

import (
	"encoding/json"
	"fmt"
)

// turnstileBootstrap renders a Turnstile widget and parks the result on
// window.__postern for the solve loop to poll.
func turnstileBootstrap(req Request) (string, error) {
	params := map[string]string{"sitekey": req.SiteKey}
	if req.Action != "" {
		params["action"] = req.Action
	}
	if req.CData != "" {
		params["cData"] = req.CData
	}

	encoded, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("solver: encode widget params: %w", err)
	}

	return fmt.Sprintf(`(() => {
  window.__postern = { token: '', error: '' };
%s

  window.__posternRender = () => {
    window.turnstile.render('#postern-widget', Object.assign(%s, {
      callback: (token) => { window.__postern.token = token; },
      'error-callback': (code) => { window.__postern.error = String(code || 'unknown'); },
    }));
  };

  // render=explicit keeps the API from rendering anything it finds on the
  // page; the only widget we want is the one __posternRender puts up.
  const script = document.createElement('script');
  script.src = 'https://challenges.cloudflare.com/turnstile/v0/api.js?onload=__posternRender&render=explicit';
  script.async = true;
  script.onerror = () => { window.__postern.error = 'api-script-blocked'; };
  document.head.appendChild(script);
})()`, hostSetup, encoded), nil
}
