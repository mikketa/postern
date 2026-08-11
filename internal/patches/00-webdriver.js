// navigator.webdriver
//
// Chrome launched with --disable-blink-features=AutomationControlled already
// reports false here, so in the default Postern setup this patch is a no-op.
// It stays as a guard for the cases where the flag is dropped (custom flags,
// an attached browser we did not launch ourselves).
//
// The conditional matters: redefining the property when it is already false
// leaves a descriptor that does not match a stock browser, which is itself
// detectable. Only touch it when it is actually wrong.
(() => {
  if (navigator.webdriver !== true) return;

  const proto = Object.getPrototypeOf(navigator);
  if (Object.getOwnPropertyDescriptor(proto, 'webdriver')) {
    delete proto.webdriver;
  }
})();
