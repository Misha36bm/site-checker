# Chromium, reachable over CDP on port 9222 from the browser network only.
FROM chromedp/headless-shell:157.0.8103.27

RUN useradd --system --uid 10002 --no-create-home chrome
USER chrome
ENV HOME=/tmp

# --no-sandbox: Chromium's sandbox needs privileges this container deliberately
# lacks. The container is the sandbox: unprivileged user, no capabilities, read-only
# root filesystem, and no network route except the egress proxy.
#
# --proxy-bypass-list=<-loopback> sends even loopback through the proxy, which
# refuses it, so a page cannot reach CDP on 127.0.0.1. The WebRTC flag closes the
# one path that would not otherwise use the proxy. The port must match
# BROWSER_PROXY_LISTEN.
CMD ["--proxy-server=http://api:3128", \
     "--proxy-bypass-list=<-loopback>", \
     "--force-webrtc-ip-handling-policy=disable_non_proxied_udp", \
     "--user-data-dir=/tmp/chrome", \
     "--disable-background-networking", \
     "--disable-component-update", \
     "--disable-default-apps", \
     "--disable-extensions", \
     "--disable-sync", \
     "--mute-audio", \
     "--no-first-run"]
