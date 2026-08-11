package solver

import (
	"encoding/json"
	"fmt"
	"net/url"
)

// recaptchaAPI is the script every reCAPTCHA flow loads. www.google.com is used
// rather than www.recaptcha.net: sites overwhelmingly embed the former, and the
// point is to look like the page we are standing in for.
const recaptchaAPI = "https://www.google.com/recaptcha/api.js"

// defaultV3Action is what sites use when they set no action of their own.
const defaultV3Action = "submit"

const (
	// apiPollMS is how often the v3 bootstrap checks whether grecaptcha has
	// finished loading.
	apiPollMS = 100

	// apiReadyAttempts bounds that wait at roughly 20 seconds, after which the
	// script is never arriving and saying so beats timing out silently.
	apiReadyAttempts = 200
)

// recaptchaV2Bootstrap renders a checkbox widget. The token arrives through the
// callback once the box is ticked — which is the click the solve loop makes.
func recaptchaV2Bootstrap(req Request) (string, error) {
	key, err := json.Marshal(req.SiteKey)
	if err != nil {
		return "", fmt.Errorf("solver: encode sitekey: %w", err)
	}

	return fmt.Sprintf(`(() => {
  window.__postern = { token: '', error: '' };
%s

  window.__posternRender = () => {
    window.__posternWidget = window.grecaptcha.render(document.querySelector('#postern-widget'), {
      sitekey: %s,
      callback: (token) => { window.__postern.token = token; },
      'error-callback': () => { window.__postern.error = 'error-callback'; },
      'expired-callback': () => { window.__postern.error = 'expired'; },
    });
  };

  // Clearing the widget back to an unticked checkbox, which is what the page's
  // own "please try again" button would do. A picture challenge can outlast the
  // session that served it, and starting over is an ordinary thing to do about
  // that rather than a reason to give up.
  window.__posternReset = () => {
    if (window.__posternWidget === undefined) return false;
    window.__postern.error = '';
    window.__postern.token = '';
    window.grecaptcha.reset(window.__posternWidget);
    return true;
  };

  const script = document.createElement('script');
  script.src = '%s?onload=__posternRender&render=explicit';
  script.async = true;
  script.defer = true;
  script.onerror = () => { window.__postern.error = 'api-script-blocked'; };
  document.head.appendChild(script);
})()`, hostSetup, key, recaptchaAPI), nil
}

// recaptchaInvisibleBootstrap renders an invisible widget and triggers it.
//
// There is nothing to click here: the widget is driven from JavaScript, and
// either Google is satisfied by the browser and the token comes back, or an
// image challenge appears that this solver has no answer for.
func recaptchaInvisibleBootstrap(req Request) (string, error) {
	key, err := json.Marshal(req.SiteKey)
	if err != nil {
		return "", fmt.Errorf("solver: encode sitekey: %w", err)
	}

	return fmt.Sprintf(`(() => {
  window.__postern = { token: '', error: '' };
%s

  window.__posternRender = () => {
    const id = window.grecaptcha.render(document.querySelector('#postern-widget'), {
      sitekey: %s,
      size: 'invisible',
      callback: (token) => { window.__postern.token = token; },
      'error-callback': () => { window.__postern.error = 'error-callback'; },
      'expired-callback': () => { window.__postern.error = 'expired'; },
    });
    window.grecaptcha.execute(id);
  };

  const script = document.createElement('script');
  script.src = '%s?onload=__posternRender&render=explicit';
  script.async = true;
  script.defer = true;
  script.onerror = () => { window.__postern.error = 'api-script-blocked'; };
  document.head.appendChild(script);
})()`, hostSetup, key, recaptchaAPI), nil
}

// recaptchaV3Bootstrap runs a v3 execute.
//
// v3 never challenges anyone: it returns a token carrying a score, and the
// site decides what to do with it. So a token here is not the same kind of
// success as elsewhere — getting one is easy, getting one that scores well is
// the whole game, and only the site's backend can tell you which you got.
func recaptchaV3Bootstrap(req Request) (string, error) {
	action := req.Action
	if action == "" {
		action = defaultV3Action
	}

	key, err := json.Marshal(req.SiteKey)
	if err != nil {
		return "", fmt.Errorf("solver: encode sitekey: %w", err)
	}
	encodedAction, err := json.Marshal(action)
	if err != nil {
		return "", fmt.Errorf("solver: encode action: %w", err)
	}

	return fmt.Sprintf(`(() => {
  window.__postern = { token: '', error: '' };
%s

  // The script's own load event is not the signal to use: api.js fires it once
  // the loader has arrived, while grecaptcha itself is pulled from gstatic
  // afterwards. Waiting for the function we are about to call is the only
  // honest readiness check — and unlike v2, v3 has no onload= parameter.
  let attempts = 0;
  const start = () => {
    const api = window.grecaptcha;
    if (!api || !api.ready || !api.execute) {
      if (++attempts > %d) {
        window.__postern.error = 'api-never-ready';
        return true;
      }
      return false;
    }

    try {
      api.ready(() => {
        api.execute(%s, { action: %s }).then(
          (token) => { window.__postern.token = token; },
          (err) => { window.__postern.error = String(err || 'execute-failed'); },
        );
      });
    } catch (e) {
      window.__postern.error = 'execute threw: ' + String(e);
    }
    return true;
  };

  if (!start()) {
    const timer = setInterval(() => { if (start()) clearInterval(timer); }, %d);
  }

  const script = document.createElement('script');
  script.src = '%s?render=%s';
  script.async = true;
  script.onerror = () => { window.__postern.error = 'api-script-blocked'; };
  document.head.appendChild(script);
})()`, hostSetup, apiReadyAttempts, key, encodedAction, apiPollMS, recaptchaAPI, url.QueryEscape(req.SiteKey)), nil
}
