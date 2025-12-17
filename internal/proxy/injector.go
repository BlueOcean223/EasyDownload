package proxy

import (
	"regexp"
	"strings"
	"time"
)

// JSInjector handles JavaScript injection for WeChat video capture
type JSInjector struct {
	version string // Used for JS file version control to force cache refresh
}

// NewJSInjector creates a new JSInjector instance
func NewJSInjector() *JSInjector {
	return &JSInjector{
		version: time.Now().Format("20060102150405"),
	}
}

// downloadButtonScript contains the JavaScript code for injecting download buttons
// and capturing video information. This is injected into WeChat video pages.
// Key features:
// - Only captures the current playing video (not accumulating all videos)
// - Detects video switches and updates current video info
// - Uses finderGetCommentDetail method to capture new video info
// - Adds download button to video pages
// - Supports both detail page and home page (feed) layouts
const downloadButtonScript = `
(function() {
  // Prevent multiple injections
  if (window.__easydownload_injected__) return;
  window.__easydownload_injected__ = true;

  // (no console log)

  // Diagnostic heartbeat so backend logs can confirm the script actually executed.
  function __ed_ping() {
    try {
      fetch("/res-downloader/wechat?type=ping", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          ts: Date.now(),
          href: (function(){ try { return location.href; } catch(e){ return ""; } })(),
        })
      }).catch(function(){});
    } catch (e) {}
  }
  __ed_ping();

  // Store for current video info - only keeps the current video, not all videos
  window.__easydownload_store__ = {
    currentVideo: null,
    lastVideoId: null,
    contact: null,
    worker: null,
    lastMetaById: {},
    lastEmitById: {},
    recentAccepted: {},
    // Last accepted DOM title (used to avoid binding worker-preload URLs to previous video's title)
    lastDomTitle: '',
    lastDomTitleTs: 0,
    // Context gating to avoid reporting preloaded/offscreen videos
    pageKey: '',
    lastHref: '',
    lastFocusTs: 0,
    // Last time we saw an active video start playing (used for "play-start window" gating)
    lastPlayStartTs: 0,
    // Last time we saw an active video actively playing (used as a focus/visibility proxy)
    lastPlayTs: 0,
    // Home feed: accept at most one "new video id" per play-start window
    lastPlayAcceptedId: '',
    lastPlayAcceptedTs: 0,
    // Home feed: accept at most one new video id per slide (blocks next-video prefetch)
    lastSlideAcceptedId: '',
    lastSlideChangeTs: 0,
    lastSlideIndex: -1,
    acceptedByPageKey: {},
    noKeyAccepted: { id: '', ts: 0 },
    lastEmitTs: 0
  };

  // Pending queue for delayed presentation (encfilekey+m keyed)
  window.__ed_pendingVideos__ = window.__ed_pendingVideos__ || new Map();
  var __ed_lastSlideDirection__ = 'none';
  var __ed_lastTouchY__ = null;

  // Debug logger (enable via: localStorage.setItem('__easydownload_debug__','1'))
  function __ed_isDebug() {
    try { return localStorage.getItem('__easydownload_debug__') === '1'; } catch (e) { return false; }
  }
  function __ed_log() {
    try {
      if (__ed_isDebug() && console && console.log) {
        console.log.apply(console, arguments);
      }
    } catch (e) {}
  }

  function __ed_trim(s) {
    try { return (s == null) ? '' : String(s).trim(); } catch (e) { return ''; }
  }

  function __ed_now() {
    try { return Date.now(); } catch (e) { return 0; }
  }

  function __ed_getHrefSafe() {
    try { return (location && location.href) ? String(location.href) : ''; } catch (e) { return ''; }
  }

  function __ed_parseQueryParam(href, key) {
    try {
      href = String(href || '');
      var re = new RegExp('[?&]' + key + '=([^&#]+)', 'i');
      var m = href.match(re);
      if (m && m[1]) return decodeURIComponent(m[1]);
    } catch (e) {}
    return '';
  }

  function __ed_getPageKeyFromHref(href) {
    href = href || __ed_getHrefSafe();
    var oid = __ed_parseQueryParam(href, 'oid');
    var nid = __ed_parseQueryParam(href, 'nid');
    if (!oid && !nid) return '';
    return String(oid || '') + ':' + String(nid || '');
  }

  function __ed_refreshPageKey() {
    try {
      var s = window.__easydownload_store__;
      if (!s) return '';
      var href = __ed_getHrefSafe();
      if (href && href !== s.lastHref) {
        s.lastHref = href;
        s.pageKey = __ed_getPageKeyFromHref(href);
      } else if (!s.pageKey) {
        s.pageKey = __ed_getPageKeyFromHref(href);
      }
      return s.pageKey || '';
    } catch (e) { return ''; }
  }

  function __ed_getPageKey() {
    try {
      var s = window.__easydownload_store__;
      if (!s) return '';
      if (!s.pageKey) __ed_refreshPageKey();
      return s.pageKey || '';
    } catch (e) { return ''; }
  }

  function __ed_touchFocus(reason, target) {
    try {
      var s = window.__easydownload_store__;
      if (!s) return;
      var now = __ed_now();
      s.lastFocusTs = now;

      // Only treat "play start" signals from the active (visible/playing) <video> element as a
      // new play window. Offscreen/preloaded <video> tags can emit events and must not reset gating.
      if (reason === 'playStart' || reason === 'playTick') {
        try {
          if (target && target.tagName && String(target.tagName).toLowerCase() === 'video') {
            var vAct = __ed_getActiveVideoEl();
            var actStarted = false;
            try { actStarted = !!(vAct && vAct.readyState >= 2 && (vAct.currentTime > 0 || !vAct.paused)); } catch (e0) { actStarted = false; }
            if (actStarted && vAct && target !== vAct) return;
          }
        } catch (e1) {}
      }

      if (reason === 'playStart') {
        s.lastPlayStartTs = now;
        s.lastPlayTs = now;
        s.lastPlayAcceptedId = '';
        s.lastPlayAcceptedTs = 0;
      } else if (reason === 'playTick') {
        // Keep "focused" while video is playing, but do NOT reset the play-start gating window.
        s.lastPlayTs = now;
      }
    } catch (e) {}
  }

  function __ed_recentlyFocused() {
    try {
      var s = window.__easydownload_store__;
      if (!s) return false;
      var now = __ed_now();
      var f = Number(s.lastFocusTs || 0);
      var p = Number(s.lastPlayTs || 0);
      // visible_or_recent: allow a slightly larger window for play events
      if (now - p <= 3500) return true;
      if (now - f <= 2500) return true;
    } catch (e) {}
    return false;
  }

  function __ed_normText(s) {
    s = __ed_trim(s);
    if (!s) return '';
    try { s = String(s).replace(/\s+/g, ' '); } catch (e) {}
    try { s = s.replace(/\s*(展开|收起)\s*$/g, '').trim(); } catch (e) {}
    return s;
  }

  function __ed_titleLooksLikeSame(a, b) {
    a = __ed_normText(a);
    b = __ed_normText(b);
    if (!a || !b) return false;
    if (a === b) return true;
    if (a.indexOf(b) !== -1 || b.indexOf(a) !== -1) return true;
    // Prefix-ish match for long titles
    if (a.length >= 12 && b.length >= 12) {
      var ap = a.slice(0, 12);
      var bp = b.slice(0, 12);
      if (a.indexOf(bp) === 0 || b.indexOf(ap) === 0) return true;
    }
    return false;
  }

  function __ed_getCurrentDomTitle() {
    try {
      var dm = __ed_collectDomMeta();
      if (dm && dm.title) return __ed_normText(dm.title);
    } catch (e) {}
    return '';
  }

  function __ed_computeVideoId(objectDesc) {
    try {
      if (!objectDesc || !objectDesc.media || !objectDesc.media.length) return '';
      var media = objectDesc.media[0];
      var parts = [];

      // Helper: extract stable params from WeChat video URL for deduplication
      // Based on url.md analysis: encfilekey and m are stable across sessions
      // Volatile params (token, sign, svrbypass, svrnonce, decodeKey) change on each access
      function __ed_extractStableUrlId(u) {
        u = __ed_normalizeUrl(u);
        if (!u) return '';
        try {
          var U = new URL(u, (location && location.origin) ? location.origin : undefined);
          var host = (U.hostname || '').toLowerCase();
          // Only process WeChat finder download hosts
          if (host.indexOf('finder.video.qq.com') === -1 && host.indexOf('findermp.video.qq.com') === -1) {
            return u;
          }
          // Extract only the most stable params: encfilekey and m
          // These remain constant even when user navigates away and returns
          var encfilekey = '';
          var m = '';
          try {
            U.searchParams.forEach(function(v, k) {
              try { k = String(k || '').toLowerCase(); } catch (e) { k = ''; }
              if (k === 'encfilekey') encfilekey = String(v || '');
              if (k === 'm') m = String(v || '');
            });
          } catch (e3) {}
          // If we have encfilekey, use it as primary identifier (most stable)
          if (encfilekey) {
            return 'encfilekey:' + encfilekey + (m ? ':m:' + m : '');
          }
          // Fallback: use pathname + m if available
          if (m) {
            return U.pathname + ':m:' + m;
          }
          return U.origin + U.pathname;
        } catch (e) {
          return u;
        }
      }

      // Priority 1: Use feed.id and objectNonceId (most stable, from WeChat API)
      // These are the same identifiers used by reference project wx_channels_download
      try {
        if (objectDesc.id) parts.push(String(objectDesc.id));
        if (objectDesc.objectNonceId) parts.push(String(objectDesc.objectNonceId));
      } catch (e) {}

      // Priority 2: Extract stable URL params if no API IDs available
      if (!parts.length && media && (media.url || media.urlToken)) {
        var raw = '';
        try { raw = String(media.url || '') + String(media.urlToken || ''); } catch (e) { raw = String(media.url || ''); }
        var stableId = __ed_extractStableUrlId(raw);
        parts.push(stableId || raw || String(media.url || ''));
      }

      // NOTE: Do NOT include decodeKey in ID - it changes across sessions!
      // Based on url.md analysis: decodeKey values 1681796139, 1918847917, 1584984553
      // are all for the SAME video but captured at different times

      return parts.join(':') || (media && media.url ? String(media.url) : '');
    } catch (e) { return ''; }
  }

  function __ed_shouldAcceptUpdate(objectDesc, videoId, meta) {
    try {
      var s = window.__easydownload_store__;
      if (!s) return false;
      var reject = function(reason, extra) {
        try {
          if (window.__easydownload_trace_gate__) {
            window.__easydownload_trace_gate__(reason, Object.assign({
              id: videoId || '',
              lastId: (s && s.lastVideoId) ? s.lastVideoId : '',
              source: (meta && meta.source) ? String(meta.source) : '',
              pageKey: __ed_getPageKey() || '',
              isHome: isHome
            }, extra || {}));
          }
        } catch (e) {}
        return false;
      };

      var now = __ed_now();
      var source = (meta && meta.source) ? String(meta.source) : '';
      var strong = (source === 'media_getter' || source === 'comment');
      var pageKey = __ed_getPageKey();
      var isHome = false;
      try {
        var path = (location && location.pathname) ? String(location.pathname) : '';
        isHome = path.indexOf('/pages/home') !== -1;
        // Home feed often reuses the same pageKey for multiple videos; treat it as unreliable there,
        // otherwise we "lock" to the first video and every subsequent download maps to the wrong resource.
        if (isHome) pageKey = '';
      } catch (e) {}
      // Home feed often reuses the same pageKey for multiple videos; treat it as unreliable there,
      // otherwise we "lock" to the first video and every subsequent download maps to the wrong resource.
      try {
        // (kept for backward compatibility; isHome/pageKey handled above)
      } catch (e) {}
      var focused = __ed_recentlyFocused();
      var domTitle = __ed_getCurrentDomTitle();
      var odTitle = __ed_normText(objectDesc && objectDesc.description ? objectDesc.description : '');
      var playAge = 999999;
      try {
        var ps = Number(s.lastPlayStartTs || 0) || Number(s.lastPlayTs || 0);
        playAge = now - ps;
      } catch (e0) { playAge = 999999; }
      var inPlayWindow = playAge >= 0 && playAge <= 1200;

      // Home feed: even strong sources (media_getter/comment) should not surface a different id
      // until a real slide change occurs. This blocks "next video" prefetch on the same slide.
      try {
        if (isHome && strong && videoId && s.lastVideoId && videoId !== s.lastVideoId) {
          var lastSlideId = s.lastSlideAcceptedId || '';
          if (lastSlideId && lastSlideId !== videoId) {
            // Fallback: if slide detection failed but the active video just started (ct small) OR
            // we are within play-start window, treat this as an implicit slide change and allow.
            var vSlide = __ed_getActiveVideoEl();
            var ctSlide = 999;
            try { ctSlide = vSlide ? Number(vSlide.currentTime || 0) : 999; } catch (eCt) { ctSlide = 999; }
            var allowImplicit = (ctSlide >= 0 && ctSlide <= 0.8) || (inPlayWindow && ctSlide >= 0 && ctSlide <= 1.5);
            if (allowImplicit) {
              try {
                if (window.__easydownload_trace_gate__) {
                  window.__easydownload_trace_gate__('strong_allow_implicit_slide', { ct: ctSlide, playAge: playAge, lastSlideId: lastSlideId });
                }
                s.lastSlideAcceptedId = '';
                s.lastSlideChangeTs = now;
              } catch (e2) {}
            } else {
              return reject('strong_same_slide_other_id', { lastSlideId: lastSlideId, ct: ctSlide, playAge: playAge });
            }
          }
        }
      } catch (e00) {}

      // Home feed: for non-strong sources (worker/fetch/xhr/payload), only accept a NEW id
      // right after playback starts, and accept at most one id per play window.
      try {
        if (isHome && videoId && (!s.lastVideoId || videoId !== s.lastVideoId) && !strong) {
          if (source === 'worker' || source === 'fetch' || source === 'xhr' || source === 'payload') {
            var recentlySwiped0 = false;
            try { recentlySwiped0 = (now - Number(s.lastSlideChangeTs || 0)) <= 2600; } catch (e0x) { recentlySwiped0 = false; }
            if (!inPlayWindow && !recentlySwiped0) {
              if (window.__easydownload_trace__) {
                window.__easydownload_trace__('video:reject', { reason: 'home_outside_play', id: videoId || '', src: source || '', playAge: playAge });
              }
              return reject('home_outside_play', { playAge: playAge });
            }
            if (s.lastPlayAcceptedId && s.lastPlayAcceptedId !== videoId) {
              if (window.__easydownload_trace__) {
                window.__easydownload_trace__('video:reject', { reason: 'home_play_already_accepted', id: videoId || '', kept: s.lastPlayAcceptedId || '', src: source || '' });
              }
              return reject('home_play_already_accepted', { kept: s.lastPlayAcceptedId || '' });
            }
          }
        }
      } catch (e01) {}
      // Title gating is helpful to avoid reporting preloaded/offscreen videos, but it can also
      // block real updates when API payloads omit description/title (cached/preloaded batches).
      // Allow "missing title" when we can confirm the user is interacting and the active video started.
      var titleOk = __ed_titleLooksLikeSame(domTitle, odTitle);
      // If DOM title is unavailable/filtered, trust non-empty objectDesc title (it came from WeChat APIs).
      try {
        if (!domTitle && odTitle && !__ed_isBadTitle(odTitle)) titleOk = true;
      } catch (e0) {}

      // Prefetch defense: if videoId changes but user didn't just swipe/switch, reject worker/network hints.
      // This avoids off-by-one where the NEXT video is prefetched while the current video is playing.
      try {
        if (videoId && s.lastVideoId && videoId !== s.lastVideoId && !strong) {
          var recentlySwiped = false;
          try { recentlySwiped = (now - Number(s.lastSlideChangeTs || 0)) <= 2200; } catch (e1) { recentlySwiped = false; }
          var activeTime = 999;
          try {
            var vAct = __ed_getActiveVideoEl();
            activeTime = vAct ? Number(vAct.currentTime || 0) : 999;
          } catch (e2) { activeTime = 999; }
          // On home feed, only slide-change is a reliable switch signal. currentTime<=1.5 alone
          // is too weak and will accept NEXT-video prefetches during initial playback.
          var isLikelySwitchMoment = recentlySwiped || (!isHome && (activeTime >= 0 && activeTime <= 1.5));
          if ((source === 'worker' || source === 'fetch' || source === 'xhr' || source === 'payload') && !isLikelySwitchMoment) {
            if (isHome && source === 'worker' && window.__easydownload_trace__) {
              window.__easydownload_trace__('video:reject', { reason: 'home_no_swipe', id: videoId || '', lastId: s.lastVideoId || '', ct: activeTime || 0 });
            }
            return reject('worker_no_switch', { ct: activeTime || 0, playAge: playAge });
          }
        }
      } catch (e3) {}

      // Worker messages are the most likely to include NEXT-video prefetch URLs.
      // For a NEW videoId, only accept worker updates after playback starts.
      // This avoids the classic off-by-one (download B gets C) right after swipe.
      try {
        if (source === 'worker' && videoId && s.lastVideoId && videoId !== s.lastVideoId) {
          var vw = __ed_getActiveVideoEl();
          var startedW = false;
          try { startedW = !!(vw && vw.readyState >= 2 && (vw.currentTime > 0 || !vw.paused)); } catch (e) { startedW = false; }
          if (!startedW) return reject('worker_not_started', { ct: (vw && vw.currentTime) ? vw.currentTime : 0 });
        }
      } catch (e4) {}
      // Worker messages often contain preload URLs for the NEXT video. Never bind a worker URL to the
      // current DOM title unless the DOM title has changed since the last accepted video (or we have no current yet).
      if (!isHome && !titleOk && source === 'worker' && !!domTitle && !odTitle) {
        var lastDom = __ed_normText(s.lastDomTitle || '');
        if (!s.lastVideoId) {
          titleOk = true;
        } else if (lastDom && !__ed_titleLooksLikeSame(domTitle, lastDom)) {
          titleOk = true;
        } else if (!lastDom) {
          // If we never captured a previous DOM title, allow once; it will be stored on accept.
          titleOk = true;
        }
      }
      if (!titleOk && !!domTitle && !odTitle && focused && source !== 'worker') {
        var same = !!(videoId && s.lastVideoId && videoId === s.lastVideoId);
        if (!(strong || same)) {
          // For NEW video ids: do not bind a title-less payload (usually a preload) to the current DOM title.
          // This is the main cause of "download B gets C" off-by-one issues.
          return reject('title_guard_dom_only', { domTitle: domTitle.slice(0, 80), odTitle: odTitle.slice(0, 80) });
        }
        try {
          var v0 = __ed_getActiveVideoEl();
          var started0 = false;
          try { started0 = !!(v0 && v0.readyState >= 2 && (v0.currentTime > 0 || !v0.paused)); } catch (e) { started0 = false; }
          if (started0) titleOk = true;
        } catch (e) {}
      }

      // Per-id throttle to reduce bursts on the same content.
      if (videoId) {
        if (!s.lastEmitById) s.lastEmitById = {};
        var lastById = Number(s.lastEmitById[videoId] || 0);
        if (videoId !== s.lastVideoId && lastById && (now - lastById) < 2000) return reject('per_id_throttle', { lastById: lastById });
      }

      // Always allow meta refresh for the already-accepted current video.
      if (videoId && s.lastVideoId && videoId === s.lastVideoId) return true;

      // Global throttle to avoid bursts even if gating fails (ms)
      if (now - Number(s.lastEmitTs || 0) < 350) {
        // Still allow same-video meta updates (handled above)
        return reject('global_throttle', { lastEmitTs: Number(s.lastEmitTs || 0) });
      }

      // Prefer pageKey gating when available
      if (pageKey) {
        var recent = s.recentAccepted && s.recentAccepted[pageKey] ? s.recentAccepted[pageKey] : null;
        if (recent && recent.id && videoId && recent.id === videoId && (now - Number(recent.ts || 0)) < 8000) {
          return reject('pageKey_recent_same', { recentTs: Number(recent.ts || 0), accepted: recent.id || '' });
        }
        var accepted = s.acceptedByPageKey ? s.acceptedByPageKey[pageKey] : '';
        if (accepted) {
          // Only allow updates for the accepted video of this pageKey
          if (videoId && accepted === videoId) return true;
          return reject('pageKey_locked', { accepted: accepted || '' });
        }

        // First time seeing this pageKey: require recent focus
        if (!focused) return reject('pageKey_no_focus', {});

        // Require title match for non-worker sources to avoid preloads (but allow started video w/ missing title)
        if (source && source !== 'worker') {
          if (!titleOk) return reject('pageKey_title_mismatch', { domTitle: domTitle.slice(0, 80), odTitle: odTitle.slice(0, 80) });
        } else {
          // Worker fallback: allow if active video is present and started
          var v = __ed_getActiveVideoEl();
          var started = false;
          try { started = !!(v && v.readyState >= 2 && (v.currentTime > 0 || !v.paused)); } catch (e) { started = false; }
          if (!started) return reject('pageKey_worker_not_started', {});
          if (!titleOk) {
            // If we cannot get a reliable DOM title, allow worker updates only during a likely switch moment.
            // This keeps "URL/decodeKey for current video" working while still rejecting background prefetch.
            var swOk = false;
            try {
              var rs = (now - Number(s.lastSlideChangeTs || 0)) <= 2200;
              var ct = 999;
              try { ct = v ? Number(v.currentTime || 0) : 999; } catch (e2) { ct = 999; }
              swOk = rs || (ct >= 0 && ct <= 1.5);
            } catch (e3) { swOk = false; }
            if (!swOk) return reject('pageKey_worker_no_switch', { ct: ct, rs: rs });
          }
        }

        // Accept and bind this pageKey to this videoId
        if (!s.acceptedByPageKey) s.acceptedByPageKey = {};
        s.acceptedByPageKey[pageKey] = videoId || 'unknown';
        if (!s.recentAccepted) s.recentAccepted = {};
        s.recentAccepted[pageKey] = { id: videoId || 'unknown', ts: now };
        return true;
      }

      // No pageKey: fallback to (focused + title match) and dedupe by last accepted id
      if (!focused) return reject('nokey_no_focus', {});
      if (!titleOk) {
        // Same idea as pageKey+worker fallback: allow during switch moment only.
        var swOk2 = false;
        try {
          var rs2 = (now - Number(s.lastSlideChangeTs || 0)) <= 2200;
          var v2 = __ed_getActiveVideoEl();
          var ct2 = 999;
          try { ct2 = v2 ? Number(v2.currentTime || 0) : 999; } catch (e4) { ct2 = 999; }
          // For the FIRST accept on home feed, allow title-less updates right after play starts.
          var firstHomeOk = false;
          try { firstHomeOk = !!(isHome && !s.lastVideoId && inPlayWindow); } catch (e5) { firstHomeOk = false; }
          swOk2 = rs2 || (!isHome && (ct2 >= 0 && ct2 <= 1.5)) || firstHomeOk;
        } catch (e5) { swOk2 = false; }
        if (!swOk2) return reject('nokey_title_guard', { rs: rs2, ct: ct2 });
      }
      if (!s.noKeyAccepted) s.noKeyAccepted = { id: '', ts: 0 };
      if (s.noKeyAccepted.id && s.noKeyAccepted.id === videoId && now - Number(s.noKeyAccepted.ts || 0) < 8000) {
        return reject('nokey_recent_same', { lastTs: Number(s.noKeyAccepted.ts || 0) });
      }
      s.noKeyAccepted.id = videoId || 'unknown';
      s.noKeyAccepted.ts = now;
      try {
        var rs3 = false;
        try { rs3 = (now - Number(s.lastSlideChangeTs || 0)) <= 2600; } catch (e7) { rs3 = false; }
        if (isHome && videoId && (!s.lastVideoId || videoId !== s.lastVideoId) && (inPlayWindow || rs3) && !strong) {
          s.lastPlayAcceptedId = videoId;
          s.lastPlayAcceptedTs = now;
        }
      } catch (e6) {}
      return true;
    } catch (e) {
      return false;
    }
  }

  // Keep pageKey fresh even when SPA updates URL via history.replaceState
  (function __ed_startPageKeyPoll() {
    try {
      var s = window.__easydownload_store__;
      __ed_refreshPageKey();
      var lastKey = __ed_getPageKey();
      setInterval(function() {
        try {
          var prev = lastKey;
          var k = __ed_refreshPageKey();
          if (k) lastKey = k;
          // When user swipes to a different video, WeChat updates oid/nid in URL.
          // Treat this as a "focus" signal so capture can trigger even without clicks.
          if (k && prev && k !== prev) {
            __ed_touchFocus('page');
            try {
              if (s && s.noKeyAccepted) s.noKeyAccepted = { id: '', ts: 0 };
              if (s) {
                s.lastSlideAcceptedId = '';
                s.lastSlideChangeTs = __ed_now();
              }
            } catch (e) {}
          }
        } catch (e) {}
      }, 500);
    } catch (e) {}
  })();

  // Update focus signals
  (function __ed_bindFocusSignals() {
    try {
      var touch = function() { __ed_touchFocus('ui'); };
      window.addEventListener('wheel', function(e) {
        __ed_lastSlideDirection__ = (e && e.deltaY > 0) ? 'down' : 'up';
        touch();
      }, { passive: true });
      window.addEventListener('scroll', touch, { passive: true });
      window.addEventListener('pointerdown', touch, { passive: true });
      window.addEventListener('touchstart', function(e) {
        __ed_lastTouchY__ = (e && e.touches && e.touches[0]) ? e.touches[0].clientY : null;
        touch();
      }, { passive: true });
      window.addEventListener('touchmove', function(e) {
        var y = (e && e.touches && e.touches[0]) ? e.touches[0].clientY : null;
        if (y != null && __ed_lastTouchY__ != null) {
          var dy = y - __ed_lastTouchY__;
          if (Math.abs(dy) > 2) __ed_lastSlideDirection__ = dy < 0 ? 'down' : 'up';
        }
      }, { passive: true });
      window.addEventListener('keydown', touch, { passive: true });

      // media events do not bubble, so use capture
      document.addEventListener('play', function(e) { __ed_touchFocus('playStart', e && e.target); }, true);
      document.addEventListener('playing', function(e) { __ed_touchFocus('playStart', e && e.target); }, true);
      // Keep focus fresh while playing without re-opening the play-start acceptance window.
      // Also trigger play confirmation check
      document.addEventListener('timeupdate', function(e) {
        var v = e && e.target;
        __ed_touchFocus('playTick', v);
        try {
          if (v && v.tagName === 'VIDEO' && v.currentTime > 0.5 && !v.paused) __ed_onPlayConfirmed();
        } catch (err) {}
      }, true);
    } catch (e) {}
  })();
  function __ed_normalizeUrl(u) {
    u = __ed_trim(u);
    if (!u) return '';
    if (u.indexOf('//') === 0) return 'https:' + u;
    if (u.indexOf('/') === 0) {
      try { return (location && location.origin ? location.origin : '') + u; } catch (e) { return u; }
    }
    if (u.indexOf('http://') === 0 || u.indexOf('https://') === 0) return u;
    return '';
  }
  function __ed_isValidHttpUrl(u) {
    return !!__ed_normalizeUrl(u);
  }

  function __ed_extractEncfileKey(u) {
    try {
      u = __ed_normalizeUrl(u || '');
      if (!u) return { key: '', m: '' };
      var U = new URL(u, (location && location.origin) ? location.origin : undefined);
      var key = '', m = '';
      U.searchParams.forEach(function(v, k) {
        k = String(k || '').toLowerCase();
        if (k === 'encfilekey') key = String(v || '');
        if (k === 'm') m = String(v || '');
      });
      return { key: key, m: m };
    } catch (e) { return { key: '', m: '' }; }
  }

  function __ed_getPendingKeyFromObjectDesc(obj) {
    try {
      var media = obj && obj.media && obj.media[0];
      if (!media) return '';
      var raw = '' + (media.url || '') + (media.urlToken || '');
      var parsed = __ed_extractEncfileKey(raw);
      if (parsed.key) return 'encfilekey:' + parsed.key + (parsed.m ? ':m:' + parsed.m : '');
      var vid = __ed_computeVideoId(obj);
      if (vid) return vid;
      if (media.decodeKey) return 'decodeKey:' + media.decodeKey;
      if (media.url) return media.url;
    } catch (e) {}
    return '';
  }

  function __ed_cleanupPending() {
    try {
      if (!window.__ed_pendingVideos__) return;
      var now = __ed_now();
      var expired = [];
      window.__ed_pendingVideos__.forEach(function(v, k) {
        if (now - Number(v && v.captureTs || 0) > 30000) expired.push(k);
      });
      expired.forEach(function(k) { window.__ed_pendingVideos__.delete(k); });
    } catch (e) {}
  }
  setInterval(__ed_cleanupPending, 5000);

  function __ed_enqueuePending(key, payload) {
    if (!key) return;
    try {
      if (!window.__ed_pendingVideos__) window.__ed_pendingVideos__ = new Map();
      var prev = window.__ed_pendingVideos__.get(key) || {};
      window.__ed_pendingVideos__.set(key, {
        info: payload,
        captureTs: __ed_now(),
        presented: !!prev.presented
      });
    } catch (e) {}
  }

  function __ed_postVideoInfo(payload) {
    fetch("/res-downloader/wechat?type=1", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload)
    }).catch(function(err) {
      if (window.__easydownload_trace__) window.__easydownload_trace__('video:send:fail', { err: String(err || '') });
    });
  }

  function __ed_findMatchingPending(domTitle) {
    var now = __ed_now();
    var candidates = [];
    try {
      if (!window.__ed_pendingVideos__) return null;
      window.__ed_pendingVideos__.forEach(function(v, id) {
        if (!v || v.presented) return;
        if (now - Number(v.captureTs || 0) > 10000) return;
        var titleMatch = __ed_titleLooksLikeSame(domTitle, v.info && v.info.description);
        candidates.push({ id: id, v: v, titleMatch: titleMatch ? 1 : 0, age: now - Number(v.captureTs || 0) });
      });
    } catch (e) {}
    candidates.sort(function(a, b) {
      if (a.titleMatch !== b.titleMatch) return b.titleMatch - a.titleMatch;
      return a.age - b.age;
    });
    return candidates[0] ? candidates[0].v : null;
  }

  function __ed_updateCurrentMarker() {
    try {
      var dmTitle = __ed_getCurrentDomTitle();
      var match = __ed_findMatchingPending(dmTitle);
      if (match && match.info && match.info.id && window.__easydownload_store__) {
        window.__easydownload_store__.lastVideoId = match.info.id;
      }
    } catch (e) {}
  }

  function __ed_onPlayConfirmed() {
    if (__ed_lastSlideDirection__ === 'up') {
      __ed_updateCurrentMarker();
      return;
    }
    var domTitle = __ed_getCurrentDomTitle();
    var matched = __ed_findMatchingPending(domTitle);
    if (matched && !matched.presented) {
      matched.presented = true;
      __ed_postVideoInfo(matched.info);
    }
  }

  function __ed_tryPresentIfConfirmed() {
    try {
      var v = __ed_getActiveVideoEl();
      if (v && v.currentTime > 0.5 && !v.paused) __ed_onPlayConfirmed();
    } catch (e) {}
  }

  function __ed_getActiveVideoEl() {
    try {
      var vids = document.querySelectorAll('video');
      if (!vids || !vids.length) return null;
      // Prefer a playing/started video
      for (var i = 0; i < vids.length; i++) {
        var v = vids[i];
        try {
          if (!v) continue;
          if (v.readyState >= 2 && !v.paused && v.currentTime > 0) return v;
        } catch (e) {}
      }
      // Fallback to any video on page
      return vids[0] || null;
    } catch (e) {
      return null;
    }
  }

  function __ed_getSlideInfo(container) {
    var info = { idx: 0, ok: false, y: 0, unit: '', transform: '', cssText: '' };
    if (!container) return info;
    try { info.cssText = container.style && container.style.cssText ? String(container.style.cssText) : ''; } catch (e) {}
    var t = '';
    try {
      if (info.cssText) {
        var m = info.cssText.match(/transform\s*:\s*([^;]+)/i);
        if (m && m[1]) t = m[1].trim();
        if (!t) {
          var m2 = info.cssText.match(/translate3d\([^)]+\)/i);
          if (m2 && m2[0]) t = m2[0];
        }
      }
    } catch (e1) {}
    if (!t) {
      try {
        var cs = window.getComputedStyle(container);
        t = cs && (cs.transform || cs.webkitTransform || '') || '';
      } catch (e2) {}
    }
    info.transform = t || '';
    function parseY(transformText) {
      if (!transformText) return null;
      var m = transformText.match(/translate3d\(\s*[-0-9.]+px\s*,\s*([-0-9.]+)(%|px)\s*,/i);
      if (m) return { y: parseFloat(m[1]), unit: m[2] };
      m = transformText.match(/translateY\(\s*([-0-9.]+)(%|px)\s*\)/i);
      if (m) return { y: parseFloat(m[1]), unit: m[2] };
      m = transformText.match(/translate\(\s*[^,]+,\s*([-0-9.]+)(%|px)\s*\)/i);
      if (m) return { y: parseFloat(m[1]), unit: m[2] };
      if (transformText.indexOf('matrix3d(') === 0) {
        var vals3d = transformText.slice(9, -1).split(',');
        if (vals3d.length >= 14) return { y: parseFloat(vals3d[13]), unit: 'px' };
      }
      if (transformText.indexOf('matrix(') === 0) {
        var vals = transformText.slice(7, -1).split(',');
        if (vals.length >= 6) return { y: parseFloat(vals[5]), unit: 'px' };
      }
      return null;
    }
    var parsed = parseY(info.transform);
    if (parsed && !isNaN(parsed.y)) {
      info.y = parsed.y;
      info.unit = parsed.unit || '';
      if (info.unit === '%') {
        info.idx = Math.round(Math.abs(info.y) / 100);
        info.ok = true;
      } else if (info.unit === 'px') {
        var h = 0;
        try { h = container.getBoundingClientRect().height || container.clientHeight || 0; } catch (e3) { h = 0; }
        if (h > 0) {
          info.idx = Math.round(Math.abs(info.y) / h);
          info.ok = true;
        }
      }
    }
    return info;
  }

  function __ed_getCurrentSlideItem() {
    // First, try legacy feed layout (slides-scroll / slides-item)
    try {
      var container = document.querySelector('.slides-scroll');
      if (container) {
        var info = __ed_getSlideInfo(container);
        var idx = info && typeof info.idx === 'number' ? info.idx : 0;
        var items = document.querySelectorAll('.slides-item');
        if (items && idx < items.length) return items[idx];
      }
    } catch (e) {}

    // Fallback: locate container around the active video element
    try {
      var v = __ed_getActiveVideoEl();
      if (!v) return null;
      var p = v;
      // Walk up to find a reasonably-scoped container that likely contains title/author
      for (var i = 0; i < 8 && p; i++) {
        try {
          if (p.querySelector && (
            p.querySelector('.feed-desc') ||
            p.querySelector('.desc') ||
            p.querySelector('[class*="desc"]') ||
            p.querySelector('[class*="nickname"]') ||
            p.querySelector('[class*="author"]')
          )) {
            return p;
          }
        } catch (e) {}
        p = p.parentElement;
      }
      return v.parentElement || null;
    } catch (e) {
      return null;
    }
  }

  function __ed_pickText(root, selectors, minLen, maxLen) {
    try {
      root = root || document;
      minLen = minLen || 1;
      maxLen = maxLen || 120;
      for (var i = 0; i < selectors.length; i++) {
        var el = root.querySelector(selectors[i]);
        if (!el) continue;
        var txt = __ed_trim(el.textContent || '');
        if (txt.length >= minLen && txt.length <= maxLen) return txt;
      }
    } catch (e) {}
    return '';
  }

  function __ed_isBadTitle(txt) {
    txt = __ed_trim(txt);
    if (!txt) return true;
    // Filter common browser/system UI strings that sometimes leak into DOM selection
    try {
      var low = String(txt).toLowerCase();
      // Common accessibility strings on some WeChat/Chromium builds
      if (low.indexOf('this is a modal window') !== -1) return true;
      if (low.indexOf('modal window') !== -1 && low.indexOf('this is') !== -1) return true;
      if (low.indexOf('beginning of dialog window') !== -1) return true;
      if (low.indexOf('escape will cancel') !== -1) return true;
      if (low.indexOf('cancel and close the window') !== -1) return true;
      if (low === 'play video' || low.indexOf('play video') !== -1) return true;
      if (low.indexOf('restore all settings to the default values') !== -1) return true;
      if (low.indexOf('video player is loading') !== -1) return true;
      // Player UI option text sometimes gets picked as "title" (e.g. danmaku transparency menu)
      if (low.indexOf('transparency') !== -1 && low.indexOf('opaque') !== -1) return true;
      if (low === 'transparency' || low === 'opaque' || low === 'semi-transparent' || low === 'semi transparent') return true;
    } catch (e) {}
    // Obvious UI labels / nav items (avoid picking buttons as title)
    var bad = ['下载','收藏','关注','举报','分享','更多','清晰度','倍速','设置','登录','搜索','推荐','朋友','直播','音乐','游戏','影视','美食','旅游','生活','知识','评论','转发'];
    for (var i = 0; i < bad.length; i++) {
      if (txt === bad[i]) return true;
      if (txt.indexOf(bad[i]) === 0 && txt.length <= 6) return true;
    }
    if (/^\d{1,2}:\d{2}$/.test(txt)) return true;
    if (/[0-9]{1,2}:[0-9]{2}\s*\/\s*[0-9]{1,2}:[0-9]{2}/.test(txt)) return true;
    // Playback speed menu text sometimes gets picked as title/author (e.g. "0.5x 1x 1.25x 1.5x 2x 3x")
    if (/\b0\.5x\b/.test(txt) && /\b1x\b/.test(txt) && /\b2x\b/.test(txt)) return true;
    if (/(自动续播|小窗模式|默认值)/.test(txt)) return true;
    return false;
  }

  function __ed_cleanAuthorName(a) {
    a = __ed_trim(a);
    if (!a) return '';
    // Remove common decorations
    try { a = a.replace(/[♡♥❤]+/g, '').trim(); } catch (e) {}
    // "X 等2个朋友" -> "X"
    try {
      var m = a.match(/^(.{1,24}?)\s*等\s*\d+\s*个朋友/i);
      if (m && m[1]) a = __ed_trim(m[1]);
    } catch (e) {}
    // Remove trailing hints like "展开/收起"
    try { a = a.replace(/\s*(展开|收起)\s*$/g, '').trim(); } catch (e) {}
    return a;
  }

  function __ed_isBadAuthorName(a) {
    a = __ed_trim(a);
    if (!a) return true;
    if (a.length > 40) return true;
    try {
      var low = String(a).toLowerCase();
      if (low === 'play video' || low.indexOf('play video') !== -1) return true;
      if (low.indexOf('beginning of dialog window') !== -1) return true;
      if (low.indexOf('escape will cancel') !== -1) return true;
      if (low.indexOf('video player is loading') !== -1) return true;
      if (low.indexOf('restore all settings to the default values') !== -1) return true;
    } catch (e) {}
    // Friend/social hints are NOT author
    if (/等\s*\d+\s*个朋友/.test(a)) return true;
    if (a.indexOf('个朋友') !== -1 || a.indexOf('朋友') !== -1) return true;
    if (a.indexOf('点赞') !== -1 || a.indexOf('赞') !== -1) return true;
    if (a.indexOf('展开') !== -1 || a.indexOf('收起') !== -1) return true;
    if (a.indexOf('#') !== -1) return true;
    if (a.indexOf('关注') !== -1 || a.indexOf('下载') !== -1 || a.indexOf('投诉') !== -1) return true;
    if (/^\d{1,2}:\d{2}$/.test(a)) return true;
    if (/\b0\.5x\b/.test(a) && /\b1x\b/.test(a) && /\b2x\b/.test(a)) return true;
    return false;
  }

  function __ed_pickBestText(root, selector, minLen, maxLen) {
    try {
      root = root || document;
      minLen = minLen || 2;
      maxLen = maxLen || 220;
      var nodes = root.querySelectorAll(selector);
      if (!nodes || !nodes.length) return '';
      var best = '';
      for (var i = 0; i < nodes.length; i++) {
        var el = nodes[i];
        if (!el) continue;
        // Skip hidden/aria-only nodes (they often contain accessibility hints like "This is a modal window.")
        try {
          if (el.getAttribute && String(el.getAttribute('aria-hidden') || '') === 'true') continue;
          if (el.offsetParent === null && el.getClientRects && (!el.getClientRects() || el.getClientRects().length === 0)) continue;
        } catch (e0) {}
        var txt = '';
        try { txt = el.innerText || el.textContent || ''; } catch (e) {}
        txt = __ed_trim(String(txt || '').replace(/\s+/g, ' '));
        if (txt.length < minLen || txt.length > maxLen) continue;
        if (__ed_isBadTitle(txt)) continue;
        if (txt.length > best.length) best = txt;
      }
      return best;
    } catch (e) {}
    return '';
  }

  function __ed_findDescElement(root) {
    root = root || document;
    try {
      // Prefer the classic desc containers first (more specific)
      var el = root.querySelector('.feed-desc,[class*="feed-desc"],.desc,[class*="desc"]');
      if (el) return el;
    } catch (e) {}
    try {
      // Fallback: any reasonably long text block
      var nodes = root.querySelectorAll('[class*="desc"],[class*="content"],p,div');
      if (!nodes || !nodes.length) return null;
      var bestEl = null;
      var bestLen = 0;
      for (var i = 0; i < nodes.length && i < 200; i++) {
        var n = nodes[i];
        if (!n) continue;
        var t = '';
        try { t = (n.innerText || n.textContent || ''); } catch (e) { t = ''; }
        t = __ed_trim(String(t || '').replace(/\s+/g, ' '));
        if (t.length < 8 || t.length > 260) continue;
        if (__ed_isBadTitle(t)) continue;
        if (t.length > bestLen) {
          bestLen = t.length;
          bestEl = n;
        }
      }
      return bestEl;
    } catch (e) {}
    return null;
  }

  function __ed_guessCreatorNearDesc(root) {
    root = root || document;
    try {
      var descEl = __ed_findDescElement(root);
      if (!descEl) return '';

      // Walk up a few levels; at each level, try to find nickname in previous siblings.
      var cur = descEl;
      for (var depth = 0; depth < 6 && cur; depth++) {
        var p = cur.parentElement;
        if (!p) break;

        // Search previous siblings only (avoids comment list that usually comes after desc)
        var sib = cur.previousElementSibling;
        var hops = 0;
        while (sib && hops < 10) {
          hops++;
          // Prefer explicit nickname-like elements
          var cand = __ed_pickBestText(sib, '[class*="nickname"],[class*="author"],[class*="name"],a,span,div', 1, 40);
          cand = __ed_cleanAuthorName(cand);
          if (cand && !__ed_isBadAuthorName(cand)) return cand;

          // Try avatar alt/aria-label inside this sibling, then nearby text
          try {
            var img = sib.querySelector && sib.querySelector('img[src*="wx.qlogo.cn"],img[class*="avatar"],img[class*="head"]');
            if (img) {
              var alt = __ed_cleanAuthorName(img.getAttribute('alt') || img.getAttribute('aria-label') || '');
              if (alt && !__ed_isBadAuthorName(alt)) return alt;
            }
          } catch (e) {}

          sib = sib.previousElementSibling;
        }

        cur = p;
      }
    } catch (e) {}
    return '';
  }

  function __ed_guessTitle(root) {
    root = root || document;
    // Prefer "desc" like areas; avoid generic ".title" which is too broad on Channels pages
    var t = __ed_pickBestText(root, '.feed-desc,[class*="feed-desc"],.desc,[class*="desc"]', 2, 220);
    if (t && !__ed_isBadTitle(t)) return t;
    // Fallback: some builds use caption/title-like classes without "desc"
    var t2 = __ed_pickBestText(root, '[class*="caption"],[class*="title"],[class*="content"]', 2, 220);
    if (t2 && !__ed_isBadTitle(t2)) return t2;
    // Fallback to meta title/description
    try {
      var mt = document.querySelector('meta[property=\"og:title\"]') || document.querySelector('meta[name=\"description\"]');
      if (mt && mt.content) {
        var mtc = __ed_trim(mt.content);
        if (mtc && !__ed_isBadTitle(mtc)) return mtc;
      }
    } catch (e) {}
    return '';
  }

  function __ed_guessAuthor(root) {
    root = root || document;
    // Primary selectors
    var a = __ed_pickText(root, [
      '[class*=\"nickname\"]',
      '[class*=\"author\"]',
      '[class*=\"name\"]'
    ], 1, 40);
    a = __ed_cleanAuthorName(a);
    if (a && __ed_isBadAuthorName(a)) a = '';
    if (a) return a;

    // Try around avatar image (wx.qlogo.cn)
    try {
      var img = root.querySelector('img[src*=\"wx.qlogo.cn\"],img[class*=\"avatar\"],img[class*=\"head\"]');
      if (img) {
        // Some pages store nickname in alt/aria-label
        var alt = __ed_cleanAuthorName(img.getAttribute('alt') || img.getAttribute('aria-label') || '');
        if (alt && !__ed_isBadAuthorName(alt)) return alt;

        var p = img.parentElement;
        for (var d = 0; d < 5 && p; d++) {
          // Prefer explicit nickname-like elements; fallback to any span/div text
          var cand = __ed_pickBestText(p, '[class*=\"nickname\"],[class*=\"author\"],[class*=\"name\"],span,div', 1, 40);
          cand = __ed_cleanAuthorName(cand);
          if (cand && !__ed_isBadAuthorName(cand)) return cand;
          p = p.parentElement;
        }
      }
    } catch (e) {}

    return '';
  }

  function __ed_guessAuthorFromFollowArea(root) {
    root = root || document;
    try {
      var btns = root.querySelectorAll('button,[role=\"button\"]');
      if (!btns || !btns.length) return '';
      for (var i = 0; i < btns.length && i < 60; i++) {
        var b = btns[i];
        if (!b) continue;
        var t = '';
        try { t = __ed_trim((b.innerText || b.textContent || '').replace(/\s+/g,' ')); } catch (e) { t = ''; }
        if (!t) continue;
        // Follow buttons: 关注 / 已关注 / 互相关注
        if (t === '关注' || t === '已关注' || t.indexOf('关注') !== -1) {
          var p = b.parentElement;
          for (var d = 0; d < 6 && p; d++) {
            var cand = __ed_pickBestText(p, '[class*=\"nickname\"],[class*=\"author\"],[class*=\"name\"],a,span,div', 1, 40);
            cand = __ed_cleanAuthorName(cand);
            if (cand && !__ed_isBadAuthorName(cand)) return cand;
            p = p.parentElement;
          }
        }
      }
    } catch (e) {}
    return '';
  }

  function __ed_guessAuthorFromProfileLink(root) {
    root = root || document;
    try {
      var links = root.querySelectorAll('a[href*=\"profile\"],a[href*=\"pages/profile\"],a[href*=\"finder\"],a[href*=\"wxid\"],a[href*=\"username\"]');
      if (!links || !links.length) return '';
      for (var i = 0; i < links.length && i < 80; i++) {
        var a = links[i];
        if (!a) continue;
        var txt = '';
        try { txt = __ed_cleanAuthorName(a.innerText || a.textContent || ''); } catch (e) { txt = ''; }
        if (txt && !__ed_isBadAuthorName(txt)) return txt;
      }
    } catch (e) {}
    return '';
  }

  function __ed_guessCreator(root) {
    root = root || document;
    // 0) best-effort: find creator name near the desc/title block (avoids comment area)
    var a0 = __ed_guessCreatorNearDesc(root);
    if (a0) return a0;
    // 1) best-effort: follow area (usually adjacent to author header)
    var a1 = __ed_guessAuthorFromFollowArea(root);
    if (a1) return a1;
    // 2) profile links
    var a2 = __ed_guessAuthorFromProfileLink(root);
    if (a2) return a2;
    // 3) fallback
    return __ed_guessAuthor(root);
  }

  function __ed_guessAvatar(root) {
    root = root || document;
    try {
      var img = root.querySelector('img[class*=\"avatar\"],img[class*=\"head\"],img[src*=\"wx.qlogo.cn\"]');
      var src = '';
      try { src = (img && (img.currentSrc || img.src)) ? (img.currentSrc || img.src) : ''; } catch (e) {}
      if (!src && img) {
        try { src = img.getAttribute('data-src') || img.getAttribute('data-original') || img.getAttribute('src') || ''; } catch (e) {}
      }
      src = __ed_normalizeUrl(src);
      if (src) return src;
    } catch (e) {}
    return '';
  }

  function __ed_guessCover(root) {
    root = root || document;
    function __ed_pickUrlFromStyle(el) {
      try {
        if (!el) return '';
        var bg = '';
        try { bg = el.style && el.style.backgroundImage ? el.style.backgroundImage : ''; } catch (e) { bg = ''; }
        if (!bg) {
          try { bg = (window.getComputedStyle ? (getComputedStyle(el).backgroundImage || '') : ''); } catch (e) { bg = ''; }
        }
        if (!bg) return '';
        var m = bg.match(/url\((['"]?)(.*?)\1\)/i);
        if (m && m[2]) return __ed_normalizeUrl(m[2]);
      } catch (e) {}
      return '';
    }
    function __ed_pickUrlFromImg(img) {
      try {
        if (!img) return '';
        var src = '';
        try { src = img.currentSrc || img.src || ''; } catch (e) {}
        if (!src) {
          try { src = img.getAttribute('data-src') || img.getAttribute('data-original') || img.getAttribute('src') || ''; } catch (e) {}
        }
        src = __ed_normalizeUrl(src);
        if (!src) return '';
        if (src.indexOf('wx.qlogo.cn') !== -1) return '';
        return src;
      } catch (e) {}
      return '';
    }

    try {
      var v = root.querySelector('video') || document.querySelector('video');
      if (v) {
        var poster = '';
        try { poster = v.poster || v.getAttribute('poster') || v.getAttribute('data-poster') || ''; } catch (e) {}
        poster = __ed_normalizeUrl(poster);
        if (poster) return poster;
      }
    } catch (e) {}

    // Background-image based cover (common in some layouts)
    try {
      var bgEl = root.querySelector('[class*=\"cover\"],[class*=\"poster\"],[style*=\"background-image\"]');
      var bgUrl = __ed_pickUrlFromStyle(bgEl);
      if (bgUrl) return bgUrl;
    } catch (e) {}

    try {
      var imgs = (root.querySelectorAll && root.querySelectorAll('img')) ? root.querySelectorAll('img') : [];
      // Prefer likely cover domains first
      for (var i = 0; i < imgs.length; i++) {
        var u0 = __ed_pickUrlFromImg(imgs[i]);
        if (!u0) continue;
        if (u0.indexOf('mmbiz.qpic.cn') !== -1 || u0.indexOf('qpic.cn') !== -1 || u0.indexOf('wximg') !== -1) return u0;
      }
      for (var i = 0; i < imgs.length; i++) {
        var u1 = __ed_pickUrlFromImg(imgs[i]);
        if (u1) return u1;
      }
    } catch (e) {}
    return '';
  }

  function __ed_readVideoMeta(root) {
    root = root || document;
    var meta = { durationMs: 0, width: 0, height: 0 };
    try {
      var v = root.querySelector('video') || document.querySelector('video');
      if (v) {
        var d = Number(v.duration);
        if (isFinite(d) && d > 0) meta.durationMs = Math.floor(d * 1000);
        var w = Number(v.videoWidth), h = Number(v.videoHeight);
        if (w > 0) meta.width = w;
        if (h > 0) meta.height = h;
      }
    } catch (e) {}
    return meta;
  }

  function __ed_collectDomMeta() {
    var root = __ed_getCurrentSlideItem() || document;
    var title = __ed_guessTitle(root);
    // Hard drop obviously wrong accessibility strings (e.g. "This is a modal window.")
    try {
      if (title && __ed_isBadTitle(title)) title = '';
    } catch (e) {}
    var author = __ed_guessCreator(root);
    var avatar = __ed_guessAvatar(root);
    var cover = __ed_guessCover(root);
    var vmeta = __ed_readVideoMeta(root);
    var contact = null;
    author = __ed_cleanAuthorName(author);
    if (author && __ed_isBadAuthorName(author)) author = '';
    // Safety: DOM title heuristics sometimes pick author nickname; never treat that as the video title.
    try {
      var tn = __ed_normText(title);
      var an = __ed_normText(author);
      if (tn && an) {
        if (tn === an) title = '';
        else if (tn.indexOf(an) === 0 && tn.length <= an.length + 2) title = '';
      }
    } catch (e) {}
    if (author || avatar) {
      contact = { nickname: author || '', head_url: avatar || '' };
    }
    return {
      title: title,
      coverUrl: cover,
      contact: contact,
      durationMs: vmeta.durationMs,
      width: vmeta.width,
      height: vmeta.height
    };
  }

  function __ed_isStaticLike(url) {
    try { url = String(url || ''); } catch (e) { return true; }
    // strip query/hash
    var u = url.split('#')[0].split('?')[0];
    // common static assets
    if (/\.(js|css|png|jpe?g|gif|webp|svg|ico|woff2?|ttf|eot)(\/)?$/i.test(u)) return true;
    // avoid obvious media streams
    if (/\.(mp4|m3u8|ts|aac|mp3)(\/)?$/i.test(u)) return true;
    return false;
  }

  // Heuristic: inspect likely Finder/Channels API responses (conservative but robust for 4.1+)
  function __ed_shouldInspectUrl(url) {
    if (!url) return false;
    try { url = String(url); } catch (e) { return false; }
    if (url.indexOf('/res-downloader/wechat') !== -1) return false; // avoid self
    if (url.indexOf('/web/report-') !== -1) return false; // avoid perf/error report noise
    if (__ed_isStaticLike(url)) return false;
    // Ignore direct video streaming/CDN URLs; they are huge and not JSON
    if (url.indexOf('finder.video.qq.com') !== -1) return false;
    if (url.indexOf('findermp.video.qq.com') !== -1) return false;
    if (url.indexOf('mpvideo.qpic.cn') !== -1) return false;

    // Relative URLs on Channels pages are usually APIs
    if (url[0] === '/') return true;
    // Same-origin Channels APIs (PC web)
    if (url.indexOf('channels.weixin.qq.com') !== -1) return true;
    // Some APIs may hit weixin hosts directly
    if (url.indexOf('szextshort.weixin.qq.com') !== -1) return true;

    // Backward-compatible heuristics
    if (url.indexOf('pc_finder') !== -1) return true;
    if (url.indexOf('micromsg-bin') !== -1 && url.indexOf('finder') !== -1) return true;
    if (url.indexOf('finder') !== -1 && (url.indexOf('comment') !== -1 || url.indexOf('detail') !== -1)) return true;
    return false;
  }

  function __ed_tryParseJson(text) {
    if (!text) return null;
    if (typeof text !== 'string') return null;
    // Quick guard to avoid parsing large HTML
    var first = text.trim()[0];
    if (first !== '{' && first !== '[') return null;
    try { return JSON.parse(text); } catch (e) { return null; }
  }

  function __ed_findObjectDescDeep(root) {
    var maxNodes = 2500;
    var nodes = 0;
    var maxDepth = 7;

    function isMediaList(media) {
      return media && media.length && media[0] && (media[0].url || media[0].urlToken || media[0].decodeKey);
    }
    function looksLikeObjectDesc(od) {
      return od && isMediaList(od.media);
    }

    function visit(node, depth, parent) {
      if (!node || depth > maxDepth) return null;
      nodes++;
      if (nodes > maxNodes) return null;

      // Typical: { objectDesc, contact }
      if (node.objectDesc && looksLikeObjectDesc(node.objectDesc)) {
        var ct = node.contact || (node.objectDesc && node.objectDesc.contact) || null;
        return { objectDesc: node.objectDesc, contact: ct || null };
      }
      // Sometimes the objectDesc-like object is already the node
      if (looksLikeObjectDesc(node)) {
        var ct2 = node.contact || (parent && parent.contact) || null;
        return { objectDesc: node, contact: ct2 || null };
      }

      if (Array.isArray(node)) {
        for (var i = 0; i < node.length; i++) {
          var r = visit(node[i], depth + 1, node);
          if (r) return r;
        }
        return null;
      }

      if (typeof node === 'object') {
        for (var k in node) {
          if (!Object.prototype.hasOwnProperty.call(node, k)) continue;
          // Skip obviously large/non-JSON-friendly fields
          if (k === 'rawData' || k === 'html' || k === 'text' || k === 'buffer' || k === 'base64' || k === 'svg') continue;
          var v = node[k];
          if (typeof v === 'string' && v.length > 200000) continue;
          var r2 = visit(v, depth + 1, node);
          if (r2) return r2;
        }
      }
      return null;
    }

    return visit(root, 0, null);
  }

  // Extract objectDesc + contact from different response shapes
  function __ed_extractObjectDescAndContact(payload) {
    if (!payload) return null;
    var objDesc = null;
    var contact = null;

    // Typical: { data: { object: { objectDesc, contact } } }
    if (payload.data && payload.data.object) {
      if (payload.data.object.objectDesc) objDesc = payload.data.object.objectDesc;
      if (payload.data.object.contact) contact = payload.data.object.contact;
      // Sometimes nested: data.object.object.objectDesc
      if (!objDesc && payload.data.object.object && payload.data.object.object.objectDesc) {
        objDesc = payload.data.object.object.objectDesc;
      }
      if (!contact && payload.data.object.object && payload.data.object.object.contact) {
        contact = payload.data.object.object.contact;
      }
    }

    // Other variants
    if (!objDesc && payload.data && payload.data.objectDesc) objDesc = payload.data.objectDesc;
    if (!objDesc && payload.objectDesc) objDesc = payload.objectDesc;
    if (!contact && payload.data && payload.data.contact) contact = payload.data.contact;
    if (!contact && payload.contact) contact = payload.contact;

    // Sometimes objectDesc itself contains contact
    if (objDesc && objDesc.contact && !contact) contact = objDesc.contact;

    // objectDesc might be stringified JSON in some edge cases
    if (objDesc && typeof objDesc === 'string') {
      var parsed = __ed_tryParseJson(objDesc);
      if (parsed) objDesc = parsed;
    }

    // Deep fallback: walk JSON to find objectDesc if shapes change in 4.1+
    if (!objDesc) {
      var deep = __ed_findObjectDescDeep(payload);
      if (deep && deep.objectDesc) {
        objDesc = deep.objectDesc;
        if (!contact) contact = deep.contact || null;
      }
    }

    if (!objDesc) return null;
    return { objectDesc: objDesc, contact: contact || null };
  }

  function __ed_tryUpdateFromPayload(payload, fromUrl, meta) {
    try {
      var extracted = __ed_extractObjectDescAndContact(payload);
      if (!extracted || !extracted.objectDesc) return false;
      var od = extracted.objectDesc;
      if (!od.media || !od.media.length) return false;
      if (window.__easydownload_updateVideo__) {
        var m = meta || {};
        if (!m.source) m.source = 'payload';
        if (fromUrl && !m.fromUrl) m.fromUrl = fromUrl;
        window.__easydownload_updateVideo__(od, extracted.contact, m);
        __ed_log('[EasyDownload][hook] updateVideo from', fromUrl || '', od.description || '');
        return true;
      }
      return false;
    } catch (e) {
      return false;
    }
  }

  // Low-frequency trace helper for backend logs
  window.__easydownload_trace__ = (function() {
    var lastTs = 0;
    var count = 0;
    return function(eventName, data) {
      try {
        if (!eventName) return;
        var now = Date.now();
        // Allow denser tracing for debugging: shorter throttle and higher cap.
        if (now - lastTs < 300) return; // throttle
        if (count >= 80) return; // cap to avoid spam
        lastTs = now;
        count++;
        fetch("/res-downloader/wechat?type=trace", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            ts: now,
            event: String(eventName),
            href: (function(){ try { return location.href; } catch(e){ return ""; } })(),
            data: data || null
          })
        }).catch(function(){});
      } catch (e) {}
    };
  })();

  // Gate-specific trace helper (separate budget, milder throttle)
  window.__easydownload_trace_gate__ = (function() {
    var lastTs = 0;
    var count = 0;
    return function(reason, data) {
      try {
        if (!reason) return;
        var now = Date.now();
        if (now - lastTs < 120) return; // finer throttle for gating
        if (count >= 200) return;       // prevent runaway
        lastTs = now;
        count++;
        fetch("/res-downloader/wechat?type=trace", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            ts: now,
            event: String(reason),
            href: (function(){ try { return location.href; } catch(e){ return ""; } })(),
            data: data || null
          })
        }).catch(function(){});
      } catch (e) {}
    };
  })();

  // Allow injected bundle code to try updating from any runtime object (not only network payloads).
  window.__easydownload_tryUpdateFromAny__ = function(anyObj, hint) {
    try {
      // If it's a JSON string, try parsing first
      if (typeof anyObj === 'string') {
        var s = anyObj.trim();
        if (s && (s[0] === '{' || s[0] === '[')) {
          try { anyObj = JSON.parse(s); } catch (e) {}
        }
      }

      var extracted = __ed_findObjectDescDeep(anyObj);
      if (extracted && extracted.objectDesc && window.__easydownload_updateVideo__) {
        window.__easydownload_updateVideo__(extracted.objectDesc, extracted.contact, { source: String(hint || 'any') });
        if (window.__easydownload_trace__) {
          window.__easydownload_trace__('tryUpdateFromAny:hit', { hint: hint || '', desc: extracted.objectDesc.description || '' });
        }
        return true;
      }
    } catch (e) {}
    return false;
  };

  // Hook fetch to inspect JSON responses for objectDesc
  function __ed_hookFetch() {
    if (window.__easydownload_fetch_hooked__) return;
    window.__easydownload_fetch_hooked__ = true;
    if (!window.fetch) return;

    var __origFetch = window.fetch;
    window.fetch = function(input, init) {
      var url = '';
      try {
        if (typeof input === 'string') url = input;
        else if (input && input.url) url = input.url;
      } catch (e) {}

      var p = __origFetch.apply(this, arguments);
      // Preserve original behavior; attach side-effect only
      return Promise.resolve(p).then(function(resp) {
        try {
          if (!resp || !__ed_shouldInspectUrl(url) || !resp.clone) return resp;
          // Try JSON first if content-type suggests it, else fallback to text parse
          var ct = '';
          try { ct = (resp.headers && resp.headers.get) ? (resp.headers.get('content-type') || '') : ''; } catch (e) {}
          if (ct.indexOf('json') !== -1) {
            resp.clone().json().then(function(data) {
              __ed_tryUpdateFromPayload(data, url, { source: 'fetch', fromUrl: url || '' });
            }).catch(function() {});
          } else {
            resp.clone().text().then(function(text) {
              var data = __ed_tryParseJson(text);
              if (data) __ed_tryUpdateFromPayload(data, url, { source: 'fetch', fromUrl: url || '' });
            }).catch(function() {});
          }
        } catch (e) {}
        return resp;
      });
    };
  }

  // Hook XHR to inspect responseText/response for objectDesc
  function __ed_hookXHR() {
    if (window.__easydownload_xhr_hooked__) return;
    window.__easydownload_xhr_hooked__ = true;
    if (!window.XMLHttpRequest) return;

    var XHR = window.XMLHttpRequest;
    var __origOpen = XHR.prototype.open;
    var __origSend = XHR.prototype.send;

    XHR.prototype.open = function(method, url) {
      try { this.__easydownload_url__ = url; } catch (e) {}
      return __origOpen.apply(this, arguments);
    };

    XHR.prototype.send = function(body) {
      try {
        var xhr = this;
        xhr.addEventListener('load', function() {
          try {
            var url = xhr.__easydownload_url__ || '';
            if (!__ed_shouldInspectUrl(url)) return;
            if (xhr.responseType === 'json' && xhr.response) {
              __ed_tryUpdateFromPayload(xhr.response, url, { source: 'xhr', fromUrl: url || '' });
              return;
            }
            var text = '';
            if (xhr.responseType === '' || xhr.responseType === 'text' || typeof xhr.responseType === 'undefined') {
              text = xhr.responseText;
            }
            var data = __ed_tryParseJson(text);
            if (data) __ed_tryUpdateFromPayload(data, url, { source: 'xhr', fromUrl: url || '' });
          } catch (e) {}
        });
      } catch (e) {}
      return __origSend.apply(this, arguments);
    };
  }

  // Hook Worker messages: WeChat Channels 4.1+ often moves feed/data logic into WebWorkers.
  // By observing message payloads flowing back to the main thread, we can still locate objectDesc/media
  // without MITM-ing video streaming domains or modifying many bundles.
  function __ed_hookWorkerMessages() {
    if (window.__easydownload_worker_hooked__) return;
    window.__easydownload_worker_hooked__ = true;
    try {
      if (typeof window.Worker !== 'function') return;
      var proto = window.Worker && window.Worker.prototype;
      if (!proto || !proto.addEventListener) return;

      // Less intrusive than replacing window.Worker (some builds may break playback if Worker is overridden).
      var __origAdd = proto.addEventListener;
      if (!proto.__easydownload_add_patched__) {
        proto.__easydownload_add_patched__ = true;
        proto.addEventListener = function(type, listener, options) {
          try {
            if (String(type) === 'message' && typeof listener === 'function') {
              var wrapped = function(ev) {
                try { __ed_handleWorkerMessage(ev); } catch (e) {}
                return listener.call(this, ev);
              };
              return __origAdd.call(this, type, wrapped, options);
            }
          } catch (e) {}
          return __origAdd.apply(this, arguments);
        };
      }

      // Try to wrap onmessage assignment if the engine exposes accessor on prototype
      try {
        var d = Object.getOwnPropertyDescriptor(proto, 'onmessage');
        if (d && d.configurable && typeof d.set === 'function' && !proto.__easydownload_onmessage_patched__) {
          proto.__easydownload_onmessage_patched__ = true;
          var __origSet = d.set;
          var __origGet = d.get;
          Object.defineProperty(proto, 'onmessage', {
            configurable: true,
            enumerable: d.enumerable,
            get: function() {
              try { return __origGet ? __origGet.call(this) : undefined; } catch (e) { return undefined; }
            },
            set: function(fn) {
              try {
                if (typeof fn === 'function') {
                  var self = this;
                  var wrapped = function(ev) {
                    try { __ed_handleWorkerMessage(ev); } catch (e) {}
                    return fn.call(self, ev);
                  };
                  return __origSet.call(this, wrapped);
                }
              } catch (e) {}
              return __origSet.call(this, fn);
            }
          });
        }
      } catch (e) {}
    } catch (e) {}
  }

  function __ed_handleWorkerMessage(ev) {
    try {
      if (!ev) return;
      var data = ev.data;
      if (!data) return;

      // Try to recover downloadable URL + decode seed from worker payloads.
      try {
        if (!window.__easydownload_store__.worker) {
          window.__easydownload_store__.worker = { seed: null, lastUrlByPageKey: {}, lastTsByPageKey: {} };
        }
        var wk = window.__easydownload_store__.worker;

        // Save latest seed if present
        if (data && typeof data === 'object' && data.seed != null) {
          try { wk.seed = String(data.seed); } catch (e) {}
        }

        // Some payloads have url but no seed; reuse last seed
        var u = null;
        if (data && typeof data === 'object' && data.url && typeof data.url === 'string') {
          u = data.url;
        }

        // Heuristic: only accept likely VOD download URLs from worker payloads.
        // IMPORTANT: worker messages often describe chunked downloads; those URLs can be incomplete
        // and may lead to corrupted downloads if surfaced to UI. Be strict here.
        if (u && (
          (u.indexOf('finder.video.qq.com') !== -1 || u.indexOf('findermp.video.qq.com') !== -1) &&
          u.indexOf('stodownload') !== -1 &&
          u.indexOf('encfilekey=') !== -1 &&
          u.indexOf('startIdx=') === -1 &&
          u.indexOf('size=') === -1
        )) {
          // Only accept worker updates when the user is actively interacting / recently played.
          if (!__ed_recentlyFocused()) return;

          var pk = __ed_getPageKey() || '';
          if (!wk.lastUrlByPageKey) wk.lastUrlByPageKey = {};
          if (!wk.lastTsByPageKey) wk.lastTsByPageKey = {};

          var key = pk || '__nokey__';
          var lastU = wk.lastUrlByPageKey[key] || '';
          var lastT = Number(wk.lastTsByPageKey[key] || 0);
          var now = __ed_now();

          // Avoid spamming updates for the same URL; also throttle per pageKey
          if (lastU !== u && (now - lastT) > 600) {
            wk.lastUrlByPageKey[key] = u;
            wk.lastTsByPageKey[key] = now;

            var seed = wk.seed;
            // Build a minimal objectDesc-like shape for existing updateCurrentVideo pipeline
            if (seed) {
              var domT = '';
              try { domT = __ed_getCurrentDomTitle(); } catch (e) { domT = ''; }
              try { if (domT && __ed_isBadTitle(domT)) domT = ''; } catch (e) {}
              var fake = {
                // IMPORTANT: never bind worker URLs to the current DOM title here.
                // Worker URLs frequently belong to NEXT-video prefetch and will cause off-by-one.
                description: '',
                media: [{
                  url: u,
                  urlToken: '',
                  decodeKey: seed,
                  coverUrl: '',
                  // worker payload "size" is often a chunk size, not total file size. Let backend probe.
                  fileSize: 0,
                  mediaType: 4,
                  spec: []
                }]
              };
              if (window.__easydownload_updateVideo__) {
                window.__easydownload_updateVideo__(fake, null, { source: 'worker' });
                if (window.__easydownload_trace__) {
                  window.__easydownload_trace__('worker:url_seed:hit', { hasSeed: true, url: u.slice(0, 120) });
                }
              }
            } else if (window.__easydownload_trace__) {
              window.__easydownload_trace__('worker:url_seed:miss_seed', { url: u.slice(0, 120) });
            }
          }
        }
      } catch (e) {}

      // Summarize message shape for debugging (very small payload)
      var summary = null;
      try {
        summary = { t: typeof data };
        if (data && data.constructor && data.constructor.name) summary.ctor = data.constructor.name;
        if (typeof data === 'string') {
          summary.len = data.length;
          summary.sample = data.slice(0, 160);
        } else if (Array.isArray(data)) {
          summary.len = data.length;
          summary.sample = data.slice(0, 3);
        } else if (data && typeof data === 'object') {
          // Typed arrays / ArrayBuffer
          if (typeof data.byteLength === 'number') summary.byteLength = data.byteLength;
          if (data.buffer && typeof data.buffer.byteLength === 'number') summary.bufferByteLength = data.buffer.byteLength;
          // Keys (cap)
          try {
            summary.keys = Object.keys(data).slice(0, 20);
          } catch (e) {}
        }
      } catch (e) {}

      // Try deep update from worker message payloads
      if (window.__easydownload_tryUpdateFromAny__) {
        var hit = window.__easydownload_tryUpdateFromAny__(data, 'worker:message');
        if (hit && window.__easydownload_trace__) {
          window.__easydownload_trace__('worker:message:hit', summary);
        } else if (!hit && window.__easydownload_trace__) {
          window.__easydownload_trace__('worker:message:miss', summary);
        }
      }
    } catch (e) {}
  }

  // Utility: Find element with retry
  function findElement(selector, maxRetries) {
    maxRetries = maxRetries || 10;
    return new Promise(function(resolve) {
      var count = 0;
      var timer = setInterval(function() {
        count++;
        var elm = typeof selector === 'function' ? selector() : document.querySelector(selector);
        if (elm) {
          clearInterval(timer);
          resolve(elm);
          return;
        }
        if (count >= maxRetries) {
          clearInterval(timer);
          resolve(null);
        }
      }, 200);
    });
  }

  // Build profile object from objectDesc and contact (similar to wx_channels_download)
  function buildVideoProfile(objectDesc, contact) {
    if (!objectDesc || !objectDesc.media || objectDesc.media.length === 0) {
      return null;
    }
    
    var media = objectDesc.media[0];
    var profile = {
      type: media.mediaType === 9 ? 'picture' : 'media',
      title: objectDesc.description || '',
      coverUrl: media.coverUrl || '',
      url: media.url + (media.urlToken || ''),
      size: media.fileSize ? Number(media.fileSize) : 0,
      key: media.decodeKey || '',
      id: objectDesc.id || '',
      spec: media.spec || [],
      contact: contact || null
    };
    
    // Extract duration from spec if available
    if (media.spec && media.spec.length > 0) {
      profile.durationMs = media.spec[0].durationMs || 0;
      profile.width = media.spec[0].width || 0;
      profile.height = media.spec[0].height || 0;
    }
    
    return profile;
  }

  // Update current video info - replaces previous video, doesn't accumulate
  function updateCurrentVideo(objectDesc, contact, meta) {
    if (!objectDesc || !objectDesc.media || objectDesc.media.length === 0) {
      return;
    }

    // Deep copy to avoid modifying original page data
    try {
      objectDesc = JSON.parse(JSON.stringify(objectDesc));
      if (contact) contact = JSON.parse(JSON.stringify(contact));
    } catch (e) {}

    // Sanitize obviously wrong titles coming from accessibility overlays
    try {
      if (__ed_isBadTitle(objectDesc.description || '')) {
        objectDesc.description = '';
      } else {
        objectDesc.description = __ed_trim(objectDesc.description || '');
      }
    } catch (e) {}

    var media = objectDesc.media[0];
    var m = meta || {};
    var source = m && m.source ? String(m.source) : '';
    var descFromDom = false;
    var hadDesc = false;
    try { hadDesc = !!__ed_trim(objectDesc.description); } catch (e) { hadDesc = false; }
    var isHome = false;
    try {
      var path0 = (location && location.pathname) ? String(location.pathname) : '';
      isHome = path0.indexOf('/pages/home') !== -1;
    } catch (e) { isHome = false; }
    var isStrong = (source === 'media_getter' || source === 'comment');

    // Guard: only surface likely VOD URLs; reject worker/edge URLs that frequently lead to corrupt downloads.
    try {
      var fullU = '';
      try { fullU = __ed_normalizeUrl(String((media && media.url) ? media.url : '') + String((media && media.urlToken) ? media.urlToken : '')); } catch (e) { fullU = ''; }
      // Skip obvious live/stream playlist formats early
      if (fullU) {
        try {
          var lu = String(fullU).toLowerCase();
          if (lu.indexOf('.m3u8') !== -1 || lu.indexOf('.flv') !== -1 || lu.indexOf('.mpd') !== -1) return;
        } catch (e2) {}
      }
      if (fullU && (fullU.indexOf('finder.video.qq.com') !== -1 || fullU.indexOf('findermp.video.qq.com') !== -1)) {
        if (fullU.indexOf('stodownload') === -1 || fullU.indexOf('encfilekey=') === -1) return;
        // reject chunk-ish URLs that are known to be incomplete
        if (fullU.indexOf('startIdx=') !== -1 || fullU.indexOf('size=') !== -1) return;
      }
    } catch (e) {}

    // Compute stable id for gating BEFORE any DOM merge; otherwise a worker-preload URL can get
    // incorrectly paired with the previous video's DOM title.
    var videoIdPre = __ed_computeVideoId(objectDesc);
    // Drop updates that are not the current (visible/recent) video
    if (!__ed_shouldAcceptUpdate(objectDesc, videoIdPre, { source: source })) {
      return;
    }
    try { window.__easydownload_store__.lastEmitTs = __ed_now(); } catch (e) {}

    // After gating: Fill missing metadata from DOM (title/author/cover/duration/resolution).
    // This reduces false negatives when network payloads omit description/title (cached/preload flows),
    // but only after we've confirmed this update belongs to the current/visible video.
    try {
      var dm = __ed_collectDomMeta();
      if (dm) {
        // Never use DOM title as a fallback for worker messages (they are frequently prefetch/next-video hints).
        var allowDomTitle = (source !== 'worker');
        // Home feed: never bind DOM metadata to NEW ids from non-strong sources. This is the main
        // reason "resource is b/c but title/cover is a" duplicates appear.
        try {
          var sH = window.__easydownload_store__;
          var sameAsLast = !!(sH && sH.lastVideoId && videoIdPre && videoIdPre === sH.lastVideoId);
          if (isHome && !sameAsLast && !isStrong && source !== 'worker') {
            allowDomTitle = false;
          }
        } catch (e0) {}
        if (!allowDomTitle) {
          // Worker is often the first signal; allow DOM title only after playback starts to reduce prefetch risk.
          try {
            var vAct2 = __ed_getActiveVideoEl();
            var started2 = !!(vAct2 && vAct2.readyState >= 2 && (vAct2.currentTime > 0 || !vAct2.paused));
            // Home feed: only allow worker DOM title near play start (prevents next-video prefetch).
            var sP = window.__easydownload_store__;
            var playAge = 999999;
            try {
              var ps2 = Number(sP.lastPlayStartTs || 0) || Number(sP.lastPlayTs || 0);
              playAge = __ed_now() - ps2;
            } catch (e3) { playAge = 999999; }
            if (started2 && (!isHome || (playAge >= 0 && playAge <= 1200))) allowDomTitle = true;
          } catch (e) {}
        }
        if (allowDomTitle && !hadDesc && __ed_trim(dm.title) && !__ed_isBadTitle(dm.title)) {
          objectDesc.description = dm.title;
          descFromDom = true;
        }
        // For worker signals, never merge DOM cover/author immediately (DOM often still shows previous slide).
        // Delayed meta refresh will re-run updateCurrentVideo once DOM is stable.
        if (source !== 'worker') {
          // Home feed: only merge DOM cover for strong sources or same-video meta refresh.
          var allowCover = true;
          try {
            var sC = window.__easydownload_store__;
            var sameAsLast2 = !!(sC && sC.lastVideoId && videoIdPre && videoIdPre === sC.lastVideoId);
            if (isHome && !sameAsLast2 && !isStrong) allowCover = false;
          } catch (e1) { allowCover = true; }
          if (media && !__ed_trim(media.coverUrl) && __ed_trim(dm.coverUrl)) {
            if (allowCover) media.coverUrl = dm.coverUrl;
          }
        }
        // Merge author/contact info from DOM if missing (but ignore friend/social hints)
        if (source !== 'worker' && dm.contact) {
          // Home feed: only merge DOM author for strong sources or same-video meta refresh.
          var allowAuthor = true;
          try {
            var sA = window.__easydownload_store__;
            var sameAsLast3 = !!(sA && sA.lastVideoId && videoIdPre && videoIdPre === sA.lastVideoId);
            if (isHome && !sameAsLast3 && !isStrong) allowAuthor = false;
          } catch (e2) { allowAuthor = true; }
          if (!allowAuthor) {
            // no-op
          } else {
          try {
            var dn = dm.contact.nickname ? __ed_cleanAuthorName(dm.contact.nickname) : '';
            var da = dm.contact.head_url ? __ed_trim(dm.contact.head_url) : '';
            var cn = (contact && contact.nickname) ? __ed_trim(contact.nickname) : '';
            var ca = (contact && (contact.head_url || contact.headUrl)) ? __ed_trim(contact.head_url || contact.headUrl) : '';
            if (!contact) {
              // only accept nickname if it looks like a real author name
              if (dn && __ed_isBadAuthorName(dn)) dn = '';
              contact = { nickname: dn || '', head_url: da || '' };
            } else {
              if (dn && __ed_isBadAuthorName(dn)) dn = '';
              if (!cn && dn) contact.nickname = dn;
              if (!ca && da) contact.head_url = da;
            }
          } catch (e) {}
          }
        }
        if (media && (!media.spec || !media.spec.length) && (dm.durationMs || dm.width || dm.height)) {
          media.spec = [{
            fileFormat: '',
            durationMs: dm.durationMs || 0,
            width: dm.width || 0,
            height: dm.height || 0
          }];
        }
      }
    } catch (e) {}

    // Cache last accepted DOM title (for worker preload avoidance)
    try {
      var s2 = window.__easydownload_store__;
      var dt2 = __ed_normText(__ed_getCurrentDomTitle());
      if (s2 && dt2 && !__ed_isBadTitle(dt2)) {
        s2.lastDomTitle = dt2;
        s2.lastDomTitleTs = __ed_now();
      }
    } catch (e) {}

    // Prefer stable identifiers if present; fallback to url + decodeKey
    var videoId = __ed_computeVideoId(objectDesc);
    var prevMeta = null;
    try {
      if (window.__easydownload_store__ && window.__easydownload_store__.lastMetaById) {
        prevMeta = window.__easydownload_store__.lastMetaById[videoId] || null;
      }
    } catch (e) {}
    
    // If same video id, allow "meta-only" improvements (title/cover/author/spec) to refresh UI once.
    if (videoId === window.__easydownload_store__.lastVideoId) {
      try {
        var cur = window.__easydownload_store__.currentVideo || (prevMeta && prevMeta.objectDesc) || null;
        var prevContact = (prevMeta && prevMeta.contact) ? prevMeta.contact : null;
        var curTitle = cur ? __ed_trim(cur.description || '') : '';
        var nextTitle = __ed_trim(objectDesc.description || '');
        var curCover = (cur && cur.media && cur.media[0]) ? __ed_trim(cur.media[0].coverUrl || '') : '';
        var nextCover = (objectDesc.media && objectDesc.media[0]) ? __ed_trim(objectDesc.media[0].coverUrl || '') : '';
        var curNick = (window.__easydownload_store__.contact && window.__easydownload_store__.contact.nickname) ? __ed_trim(window.__easydownload_store__.contact.nickname) : (prevContact && prevContact.nickname ? __ed_trim(prevContact.nickname) : '');
        var nextNick = (contact && contact.nickname) ? __ed_trim(contact.nickname) : '';
        // duration/size/spec improvements should also refresh UI (otherwise we get "duplicates" with missing fields)
        var curDur = 0, nextDur = 0;
        var curW = 0, curH = 0, nextW = 0, nextH = 0;
        var curSize = 0, nextSize = 0;
        try {
          if (cur && cur.media && cur.media[0]) {
            try { curSize = Number(cur.media[0].fileSize || 0) || 0; } catch (e) { curSize = 0; }
            if (cur.media[0].spec && cur.media[0].spec.length) {
              try { curDur = Number(cur.media[0].spec[0].durationMs || 0) || 0; } catch (e) { curDur = 0; }
              try { curW = Number(cur.media[0].spec[0].width || 0) || 0; } catch (e) { curW = 0; }
              try { curH = Number(cur.media[0].spec[0].height || 0) || 0; } catch (e) { curH = 0; }
            }
          } else if (prevMeta && prevMeta.objectDesc && prevMeta.objectDesc.media && prevMeta.objectDesc.media[0]) {
            try { curSize = Number(prevMeta.objectDesc.media[0].fileSize || 0) || 0; } catch (e) { curSize = 0; }
            if (prevMeta.objectDesc.media[0].spec && prevMeta.objectDesc.media[0].spec.length) {
              try { curDur = Number(prevMeta.objectDesc.media[0].spec[0].durationMs || 0) || 0; } catch (e) { curDur = 0; }
              try { curW = Number(prevMeta.objectDesc.media[0].spec[0].width || 0) || 0; } catch (e) { curW = 0; }
              try { curH = Number(prevMeta.objectDesc.media[0].spec[0].height || 0) || 0; } catch (e) { curH = 0; }
            }
          }
        } catch (e) {}
        try {
          if (objectDesc && objectDesc.media && objectDesc.media[0]) {
            try { nextSize = Number(objectDesc.media[0].fileSize || 0) || 0; } catch (e) { nextSize = 0; }
            if (objectDesc.media[0].spec && objectDesc.media[0].spec.length) {
              try { nextDur = Number(objectDesc.media[0].spec[0].durationMs || 0) || 0; } catch (e) { nextDur = 0; }
              try { nextW = Number(objectDesc.media[0].spec[0].width || 0) || 0; } catch (e) { nextW = 0; }
              try { nextH = Number(objectDesc.media[0].spec[0].height || 0) || 0; } catch (e) { nextH = 0; }
            }
          }
        } catch (e) {}

        var improved = false;
        if (!curTitle && nextTitle && !__ed_isBadTitle(nextTitle)) improved = true;
        if (curTitle && nextTitle && !__ed_isBadTitle(nextTitle) && nextTitle.length > curTitle.length && curTitle !== nextTitle) improved = true;
        if (!curCover && nextCover) improved = true;
        if (!curNick && nextNick) improved = true;
        if (!curDur && nextDur) improved = true;
        if ((!curW || !curH) && (nextW && nextH)) improved = true;
        if ((!curSize || curSize <= 0) && (nextSize && nextSize > 0)) improved = true;
        if (curSize > 0 && nextSize > 0 && nextSize > curSize * 1.2) improved = true;

        if (!improved) return;
      } catch (e) {
        return;
      }
    }
    
    // Update to new video - this replaces the previous video info
    window.__easydownload_store__.lastVideoId = videoId;
    window.__easydownload_store__.currentVideo = objectDesc;
    window.__easydownload_store__.contact = contact || null;
    try {
      if (!window.__easydownload_store__.lastMetaById) window.__easydownload_store__.lastMetaById = {};
      window.__easydownload_store__.lastSlideAcceptedId = videoId || '';
      var snapMedia = media ? {
        url: media.url || '',
        urlToken: media.urlToken || '',
        decodeKey: media.decodeKey || '',
        mediaType: media.mediaType || 0,
        coverUrl: media.coverUrl || '',
        fileSize: media.fileSize || 0,
        spec: media.spec ? JSON.parse(JSON.stringify(media.spec)) : []
      } : {};
      var snapContact = contact ? { nickname: contact.nickname || '', head_url: contact.head_url || contact.headUrl || '' } : null;
      window.__easydownload_store__.lastMetaById[videoId] = {
        objectDesc: { description: objectDesc.description || '', media: [snapMedia] },
        contact: snapContact,
        ts: __ed_now(),
        pageKey: __ed_getPageKey(),
        href: __ed_getHrefSafe(),
        domTitle: __ed_getCurrentDomTitle(),
        source: source || '',
        descFromDom: !!descFromDom
      };
      if (!window.__easydownload_store__.lastEmitById) window.__easydownload_store__.lastEmitById = {};
      window.__easydownload_store__.lastEmitById[videoId] = __ed_now();
    } catch (e) {}

    if (window.__easydownload_trace__) {
      window.__easydownload_trace__('video:accept', { id: videoId || '', source: source || '', descFromDom: !!descFromDom });
    }
    
    // Build full data payload with contact info
    // Final safety: sanitize contact nickname before sending
    try {
      if (contact && contact.nickname) {
        contact.nickname = __ed_cleanAuthorName(contact.nickname);
        if (__ed_isBadAuthorName(contact.nickname)) contact.nickname = '';
      }
    } catch (e) {}
    var payload = {
      // Use a stable per-video id so the UI can keep history instead of replacing entries
      id: videoId || '',
      media: objectDesc.media,
      description: objectDesc.description,
      title: (dm && dm.title) ? dm.title : '',
      domTitle: __ed_getCurrentDomTitle(),
      contact: contact,
      pageKey: __ed_getPageKey(),
      href: __ed_getHrefSafe(),
      ts: __ed_now(),
      source: source || ''
    };
    
    // Enqueue to pending queue instead of immediate POST
    var pendingKey = __ed_getPendingKeyFromObjectDesc(objectDesc) || videoId || '';
    __ed_enqueuePending(pendingKey, payload);
    if (window.__easydownload_trace__) window.__easydownload_trace__('video:pending', { key: pendingKey || '', id: videoId || '', src: source || '' });
    __ed_tryPresentIfConfirmed();

    // Delayed meta refresh: DOM text/author often appears slightly after worker url/seed message.
    try {
      if (!window.__easydownload_store__.metaRefresh) window.__easydownload_store__.metaRefresh = {};
      var k = videoId || (media && media.url ? String(media.url) : '');
      if (k && !window.__easydownload_store__.metaRefresh[k]) {
        window.__easydownload_store__.metaRefresh[k] = 1;
        setTimeout(function() {
          try {
            var dm2 = __ed_collectDomMeta();
            if (!dm2) return;
            var t2 = __ed_trim(dm2.title || '');
            var c2 = __ed_trim(dm2.coverUrl || '');
            var n2 = dm2.contact && dm2.contact.nickname ? __ed_trim(dm2.contact.nickname) : '';
            var need = false;
            if (t2 && !__ed_isBadTitle(t2) && (__ed_trim(objectDesc.description || '') !== t2)) need = true;
            if (c2 && (!media.coverUrl)) need = true;
            if (n2 && !__ed_isBadAuthorName(n2) && (!contact || !contact.nickname)) need = true;
            if (!need) return;
            // Call updateVideo again; meta-only improvements are allowed above.
            var clone = objectDesc;
            try {
              if (t2 && !__ed_isBadTitle(t2)) clone.description = t2;
              if (c2) clone.media[0].coverUrl = c2;
            } catch (e) {}
            var ctt = contact;
            if (!ctt && dm2.contact) ctt = dm2.contact;
            if (window.__easydownload_updateVideo__) window.__easydownload_updateVideo__(clone, ctt);
          } catch (e) {}
        }, 800);
      }
    } catch (e) {}
  }

  // Create download icon SVG
  function createDownloadIcon() {
    var iconHtml = '<svg class="svg-icon icon" viewBox="0 0 1024 1024" version="1.1" xmlns="http://www.w3.org/2000/svg" fill="currentColor" width="28" height="28"><path d="M213.333333 853.333333h597.333334v-85.333333H213.333333m597.333334-384h-170.666667V128H384v256H213.333333l298.666667 298.666667 298.666667-298.666667z"></path></svg>';
    var container = document.createElement('div');
    container.innerHTML = '<div class="click-box op-item item-gap-combine easydownload-btn" role="button" aria-label="下载" style="padding: 4px; cursor: pointer;">' + iconHtml + '<span class="text">下载</span></div>';
    return container.firstChild;
  }

  // Create download icon for home page (different style)
  function createDownloadIconHome() {
    var iconBase64 = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAABwAAAAcCAYAAAByDd+UAAAB9ElEQVR4AeyVa3LCMAwGk16scDLCyaAnc3ddy2PnzTDDrzIW8UP6VhIh+Ro+/PkHbjY8pXTZPNw5ON1SAdijWELTOcs8Jr4n9g7HKWARe6AWVT2ZhzEdbnzdih/T7XEILCIKCriO4zi3EfkrdseEWj3T9bELbGHjH0joQomzJ2ZLhQ7E2Y2FnxubQIJsn5UNiFmB/ruGX0AvxDtf+K8Cca4wInLWXE+NArUTOdl50AIIzMxsidB7EZjHnVqjpUbn2wFxEGZmZujN4boLcIGffwmTcrlm0RW1uvMOyEl2oCphQtlaHYvMWy/ijdUWv2UFknVUE9m1Gi/PgXqjCfWvUhOsQBSjugCz9faI5LO2ai3QdTg478wOYI6aLQtbxiXVvTaIKq1Qq+cZSETdaANmcwPdam+WmB/GByMDSyaKffu1ZsWn7UBAjv46P61eBpYNKwiRstVfgPr7ttAjmAK5CGLVH1pgzoTSFdVx1QicsBi7vmhZgJZhClYgCgZ70N3GOr1hcXfmYtSpQBdYtMsniQmw9fqwMswbyuq6tndAqrTCgFopcSnDU0r5rX5w1VeQtoCZegd0A2j+jZgLNgEDbc0Z01czzsfjhE43FsA4LWCD4o3uo+rQiHMYJzTk6nUTWD2YoOAb/ZThvjtOAXcVXjz8OPAXAAD//5jl7kwAAAAGSURBVAMA8H8MSLsb1AoAAAAASUVORK5CYII=";
    var iconHtml = '<div class="op-icon download-icon easydownload-btn" style="background-image: url(\'' + iconBase64 + '\'); width: 28px; height: 28px; background-size: contain;"></div>';
    var container = document.createElement('div');
    container.innerHTML = '<div class="click-box op-item easydownload-btn" role="button" aria-label="下载" style="padding: 4px; cursor: pointer;">' + iconHtml + '<div class="op-text">下载</div></div>';
    return container.firstChild;
  }

  // Show toast notification
  function showToast(message, duration) {
    duration = duration || 2000;
    var toast = document.createElement('div');
    toast.style.cssText = 'position:fixed;top:50%;left:50%;transform:translate(-50%,-50%);background:rgba(0,0,0,0.8);color:white;padding:12px 24px;border-radius:8px;z-index:99999;font-size:14px;';
    toast.textContent = message;
    document.body.appendChild(toast);
    setTimeout(function() {
      toast.remove();
    }, duration);
  }

  // Handle download button click
  function handleDownloadClick() {
    function __ed_pickDownloadCandidate() {
      try {
        var s = window.__easydownload_store__;
        if (!s) return null;

        var now = Date.now();
        var dm = null;
        try { dm = __ed_collectDomMeta(); } catch (e) { dm = null; }
        var domTitle = __ed_normText(dm && dm.title ? dm.title : '');
        var pageKey = __ed_getPageKey();

        // Prefer current video if it matches DOM title.
        try {
          if (s.currentVideo && s.lastVideoId) {
            var curDesc = __ed_normText(s.currentVideo.description || '');
            if (!domTitle || __ed_titleLooksLikeSame(domTitle, curDesc)) {
              return {
                id: s.lastVideoId,
                objectDesc: s.currentVideo,
                contact: s.contact || null,
                domTitle: domTitle,
                pageKey: pageKey,
                chosenSource: 'current'
              };
            }
          }
        } catch (e0) {}

        // Otherwise scan cache for a better match by DOM title / pageKey.
        var best = null;
        var bestId = '';
        var bestScore = -1;
        function scoreEntry(id, entry) {
          if (!entry || !entry.objectDesc || !entry.objectDesc.media || !entry.objectDesc.media.length) return -1;
          if (now - Number(entry.ts || 0) > 60000) return -1; // don't use stale prefetch entries
          var od = entry.objectDesc;
          var desc = __ed_normText(od.description || '');
          var entryDom = __ed_normText(entry.domTitle || '');
          if (domTitle && !__ed_titleLooksLikeSame(domTitle, entryDom || desc)) return -1;

          var sc = 0;
          if (pageKey && entry.pageKey && String(entry.pageKey) === String(pageKey)) sc += 40;
          if (domTitle) sc += 25;
          var src = entry.source ? String(entry.source) : '';
          if (src === 'media_getter' || src === 'comment') sc += 20;
          var age = Math.max(0, Math.floor((now - Number(entry.ts || 0)) / 1000));
          sc += Math.max(0, 18 - age);
          return sc;
        }
        try {
          if (s.lastMetaById) {
            for (var k in s.lastMetaById) {
              if (!Object.prototype.hasOwnProperty.call(s.lastMetaById, k)) continue;
              var e = s.lastMetaById[k];
              var sc2 = scoreEntry(k, e);
              if (sc2 > bestScore) {
                bestScore = sc2;
                best = e;
                bestId = k;
              }
            }
          }
        } catch (e1) {}

        if (best && best.objectDesc && best.objectDesc.media && best.objectDesc.media.length) {
          return {
            id: bestId,
            objectDesc: best.objectDesc,
            contact: best.contact || null,
            domTitle: domTitle,
            pageKey: pageKey,
            chosenSource: best.source || ''
          };
        }
      } catch (e) {}
      return null;
    }

    __ed_touchFocus('ui');
    var picked = __ed_pickDownloadCandidate();
    if (!picked || !picked.objectDesc) {
      showToast('未检测到视频信息，请稍后重试');
      if (window.__easydownload_trace__) window.__easydownload_trace__('download:miss', {});
      return;
    }

    var video = picked.objectDesc;
    var payload = {
      id: picked.id || '',
      media: video.media,
      description: video.description,
      contact: picked.contact || (window.__easydownload_store__ ? window.__easydownload_store__.contact : null),
      pageKey: picked.pageKey || __ed_getPageKey(),
      href: __ed_getHrefSafe(),
      ts: Date.now(),
      source: 'download_click:' + (picked.chosenSource || ''),
      domTitle: picked.domTitle || '',
      chosenSource: picked.chosenSource || ''
    };

    showToast('正在准备下载...');
    fetch("/res-downloader/wechat?type=download", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload)
    }).catch(function(err) {
      if (window.__easydownload_trace__) window.__easydownload_trace__('download:send:fail', { err: String(err || '') });
    });
    if (window.__easydownload_trace__) window.__easydownload_trace__('download:sent', { id: payload.id || '', domTitle: payload.domTitle || '', chosenSource: payload.chosenSource || '' });
  }

  // Detect video switch on home page by monitoring slide changes
  function setupVideoSwitchDetection() {
    // Monitor for slide container changes (home page feed)
    var lastSlideIndex = -1;
    setInterval(function() {
      var container = document.querySelector('.slides-scroll');
      if (!container) return;
      var info = __ed_getSlideInfo(container);
      if (!info.ok) {
        if (window.__easydownload_trace_gate__) {
          window.__easydownload_trace_gate__('slide:parse_fail', {
            css: (info.cssText || '').slice(0, 120),
            transform: (info.transform || '').slice(0, 120)
          });
        }
        return;
      }
      var idx = info.idx;

      // First observation: initialize without treating as a swipe/change. This avoids
      // misclassifying initial load as a switch moment (which lets prefetch steal "current").
      if (lastSlideIndex === -1) {
        lastSlideIndex = idx;
        try {
          var s0 = window.__easydownload_store__;
          if (s0) s0.lastSlideIndex = idx;
        } catch (e) {}
        return;
      }

      if (idx !== lastSlideIndex) {
        lastSlideIndex = idx;
        // Sliding to the next video in home feed often does NOT change URL (no oid/nid),
        // so our pageKey-based gating can get "stuck". Treat slide change as a hard context switch.
        try {
          __ed_touchFocus('page');
          var s = window.__easydownload_store__;
          if (s) {
            // Do NOT clear lastVideoId/currentVideo here; doing so allows prefetch payloads to "steal"
            // the current slot and causes the classic off-by-one (download B gets C).
            s.lastSlideIndex = idx;
            s.lastSlideChangeTs = __ed_now();
            s.lastSlideAcceptedId = '';
            try { s.lastSlideDomTitle = __ed_getCurrentDomTitle(); } catch (e2) {}
            // Reset no-key acceptance window (home feed commonly has no pageKey)
            if (s.noKeyAccepted) s.noKeyAccepted = { id: '', ts: 0 };
            // Home feed pageKey isn't per-video; reset bindings on slide change.
            try { if (s.acceptedByPageKey) s.acceptedByPageKey = {}; } catch (e) {}
            try { if (s.recentAccepted) s.recentAccepted = {}; } catch (e) {}
          }
          if (window.__easydownload_trace__) window.__easydownload_trace__('slide:change', { idx: idx, y: info.y || 0, unit: info.unit || '' });
        } catch (e) {}
        // Re-inject button for new slide
        setTimeout(function() {
          insertDownloadBtnHomePage();
        }, 300);
      }
    }, 300);
  }

  // Insert download button into home page (feed view)
  async function insertDownloadBtnHomePage() {
    var container = await findElement(function() {
      return document.querySelector('.slides-scroll');
    });
    if (!container) return false;

    // Find current slide index
    var info = __ed_getSlideInfo(container);
    var idx = info && typeof info.idx === 'number' ? info.idx : 0;

    var items = document.querySelectorAll('.slides-item');
    if (idx >= items.length) return false;

    var item = items[idx];
    if (item.querySelector('.easydownload-btn')) return true; // Already injected

    var opItem = await findElement(function() {
      return item.getElementsByClassName('click-box op-item')[0];
    });
    if (!opItem || !opItem.parentElement) return false;

    var btn = createDownloadIconHome();
    btn.onclick = handleDownloadClick;
    opItem.parentElement.appendChild(btn);
    // (no console log)
    return true;
  }

  // Insert download button into detail page
  async function insertDownloadBtnDetailPage() {
    // Try column layout first (vertical operation bar)
    var oprWrp = await findElement(function() {
      return document.getElementsByClassName('full-opr-wrp layout-col')[0];
    });
    if (oprWrp) {
      if (oprWrp.querySelector('.easydownload-btn')) return true;
      var btn = createDownloadIcon();
      btn.onclick = handleDownloadClick;
      var lastChild = oprWrp.children[oprWrp.children.length - 1];
      if (lastChild) {
        oprWrp.insertBefore(btn, lastChild);
      } else {
        oprWrp.appendChild(btn);
      }
      // (no console log)
      return true;
    }

    // Try row layout (horizontal operation bar)
    oprWrp = await findElement(function() {
      return document.getElementsByClassName('full-opr-wrp layout-row')[0];
    });
    if (oprWrp) {
      if (oprWrp.querySelector('.easydownload-btn')) return true;
      var btn = createDownloadIcon();
      btn.onclick = handleDownloadClick;
      var lastChild = oprWrp.children[oprWrp.children.length - 1];
      if (lastChild) {
        oprWrp.insertBefore(btn, lastChild);
      } else {
        oprWrp.appendChild(btn);
      }
      // (no console log)
      return true;
    }

    return false;
  }

  // Main injection function
  async function injectDownloadButton() {
    // (no console log)
    
    // Check if on home page
    if (window.location.pathname.includes('/pages/home')) {
      var success = await insertDownloadBtnHomePage();
      if (success) return;
    }

    // Try detail page injection
    await insertDownloadBtnDetailPage();
  }

  // Re-inject on navigation (for SPA)
  var lastPath = window.location.pathname;
  setInterval(function() {
    if (window.location.pathname !== lastPath) {
      lastPath = window.location.pathname;
      // Clear current video when navigating to new page
      try {
        if (window.__easydownload_store__) {
          window.__easydownload_store__.currentVideo = null;
          window.__easydownload_store__.contact = null;
          window.__easydownload_store__.lastVideoId = null;
          window.__easydownload_store__.lastSlideAcceptedId = '';
          window.__easydownload_store__.lastSlideChangeTs = 0;
          window.__easydownload_store__.lastSlideIndex = -1;
        }
      } catch (e) {}
      setTimeout(injectDownloadButton, 500);
    }
  }, 500);

  // Setup video switch detection for home page
  setupVideoSwitchDetection();

  // Install network hooks early to reliably capture objectDesc even if bundle changes
  __ed_hookFetch();
  __ed_hookXHR();
  __ed_hookWorkerMessages();

  // Initial injection after page load
  setTimeout(injectDownloadButton, 800);

  // Expose updateCurrentVideo for use by injected JS code in media getter
  window.__easydownload_updateVideo__ = updateCurrentVideo;
})();
`

// Regular expressions for JS injection
var (
	// Matches accessor getter forms:
	// - get media(){
	// - get media() {   (some builds may include parentheses even if non-standard)
	mediaGetterRegex = regexp.MustCompile(`get\s+media\s*(?:\(\s*\))?\s*\{`)

	// Matches: async finderGetCommentDetail(param){ ... }async
	// NOTE: Modeled after wx_channels_download: do not rely on `return ...` shape.
	// Use dotall + non-greedy to avoid spanning too much content.
	commentDetailRegex = regexp.MustCompile(`(?s)async\s+finderGetCommentDetail\s*\((\w+)\)\s*\{(.*?)\}\s*async`)

	// Matches JS file links in HTML - src attribute ending with .js
	jsSrcLinkRegex = regexp.MustCompile(`(src\s*=\s*["'][^"']+)\.js(["'])`)
	// Matches JS file links in HTML - href attribute ending with .js
	jsHrefLinkRegex = regexp.MustCompile(`(href\s*=\s*["'][^"']+)\.js(["'])`)

	// Safer JS import rewriting patterns (modeled after wx_channels_download):
	// Only rewrite common import/module patterns rather than replacing every `.js"` substring.
	jsDepRegex        = regexp.MustCompile(`"js/([^"']+)\.js"`)
	jsFromRegex       = regexp.MustCompile(`from\s*["']([^"']+)\.js["']`)
	jsLazyImportRegex = regexp.MustCompile(`import\(\s*["']([^"']+)\.js["']\s*\)`)
	jsImportRegex     = regexp.MustCompile(`\bimport\s*(?:[^\n;]*?\s+from\s+)?["']([^"']+)\.js["']`)
)

// GetDownloadButtonScript returns the JavaScript code for download button injection
func (ji *JSInjector) GetDownloadButtonScript() string {
	return downloadButtonScript
}

// InjectMediaCapture injects capture code into the get media() getter
// This captures the objectDesc when the media property is accessed
// The injected code uses the updateCurrentVideo function to track only the current video
// It also captures contact information from the parent object for author info
func (ji *JSInjector) InjectMediaCapture(jsContent string) string {
	if !mediaGetterRegex.MatchString(jsContent) {
		return jsContent
	}

	// Injection code that captures objectDesc and updates current video
	// Uses window.__easydownload_updateVideo__ if available (from download button script)
	// Falls back to direct fetch if not available
	// Also captures contact info from this.contact if available
	injectionCode := `get media(){
								try{
									var __ed_obj = this && (this.objectDesc || this);
									// Accept either objectDesc-like shape or direct object containing media list
									if(__ed_obj && __ed_obj.media && __ed_obj.media.length && (__ed_obj.media[0].url || __ed_obj.media[0].decodeKey || __ed_obj.media[0].urlToken)){
										var _contact = this.contact || __ed_obj.contact || null;
									if(window.__easydownload_updateVideo__){
											window.__easydownload_updateVideo__(__ed_obj, _contact, { source: 'media_getter' });
									}else{
										var _payload = {
												media: __ed_obj.media,
												description: __ed_obj.description,
											contact: _contact
										};
										fetch("/res-downloader/wechat?type=1", {
										  method: "POST",
										  headers: { "Content-Type": "application/json" },
										  body: JSON.stringify(_payload),
										});
									}
										if(window.__easydownload_trace__){
											window.__easydownload_trace__('media:getter:hit', { hasDesc: !!__ed_obj.description, hasUrl: !!(__ed_obj.media && __ed_obj.media[0] && __ed_obj.media[0].url) });
										}
									}else{
										if(window.__easydownload_tryUpdateFromAny__){
											window.__easydownload_tryUpdateFromAny__(this, 'media:getter');
										}
										if(window.__easydownload_trace__){
											window.__easydownload_trace__('media:getter:miss', { hasObjectDesc: !!(this && this.objectDesc) });
										}
									}
								}catch(e){};`

	return mediaGetterRegex.ReplaceAllString(jsContent, injectionCode)
}

// InjectCommentCapture injects capture code into the finderGetCommentDetail method
// This captures video info from comment detail responses when user switches videos
// The injected code uses the updateCurrentVideo function to track only the current video
// It also captures contact information from res.data.object.contact for author info
func (ji *JSInjector) InjectCommentCapture(jsContent string) string {
	if !commentDetailRegex.MatchString(jsContent) {
		return jsContent
	}

	// Injection code that wraps the original function body in an async arrow IIFE:
	// - Preserves lexical `this`
	// - Preserves return value (whatever original body returns)
	// - Adds best-effort extraction of objectDesc/contact
	injectionCode := `async finderGetCommentDetail($1) {
								var res = await (async () => {
									$2
								})();
								try {
									if(window.__easydownload_tryUpdateFromAny__){
										window.__easydownload_tryUpdateFromAny__(res, 'comment');
									}else{
										var _od = res?.data?.object?.objectDesc || res?.data?.object?.object?.objectDesc || null;
										if (_od) {
											var _contact = res?.data?.object?.contact || res?.data?.object?.object?.contact || null;
											if (window.__easydownload_updateVideo__) {
												window.__easydownload_updateVideo__(_od, _contact, { source: 'comment' });
											} else {
										var _payload = {
													media: _od.media,
													description: _od.description,
											contact: _contact
										};
										fetch("/res-downloader/wechat?type=2", {
										  method: "POST",
										  headers: { "Content-Type": "application/json" },
										  body: JSON.stringify(_payload),
										});
									}
								}
									}
									if(window.__easydownload_trace__){
										window.__easydownload_trace__('comment:called', { hasRes: !!res });
									}
								} catch (e) {}
								return res;
							} async`

	return commentDetailRegex.ReplaceAllString(jsContent, injectionCode)
}

// AddVersionToJSLinks adds version query parameter to ALL JS links in HTML
// This forces the browser to fetch fresh JS files that we can inject into
func (ji *JSInjector) AddVersionToJSLinks(htmlContent string) string {
	v := "?v=" + ji.version

	// Replace src="...js" with src="...js?v=VERSION"
	result := jsSrcLinkRegex.ReplaceAllString(htmlContent, `$1.js`+v+`$2`)

	// Replace href="...js" with href="...js?v=VERSION"
	result = jsHrefLinkRegex.ReplaceAllString(result, `$1.js`+v+`$2`)

	return result
}

// AddVersionToJSImports adds version parameter to .js" references inside JS content
// This ensures dynamically imported modules also bypass cache
func (ji *JSInjector) AddVersionToJSImports(jsContent string) string {
	v := "?v=" + ji.version

	result := jsContent

	// "js/xxx.js" -> "js/xxx.js?v=..."
	result = jsDepRegex.ReplaceAllString(result, `"js/$1.js`+v+`"`)

	// from "xxx.js" -> from "xxx.js?v=..."
	// (preserve quote style by using two passes)
	result = regexp.MustCompile(`from\s*"([^"]+)\.js"`).ReplaceAllString(result, `from "$1.js`+v+`"`)
	result = regexp.MustCompile(`from\s*'([^']+)\.js'`).ReplaceAllString(result, `from '$1.js`+v+`'`)

	// import("xxx.js") / import('xxx.js') -> import("xxx.js?v=...")
	result = regexp.MustCompile(`import\(\s*"([^"]+)\.js"\s*\)`).ReplaceAllString(result, `import("$1.js`+v+`")`)
	result = regexp.MustCompile(`import\(\s*'([^']+)\.js'\s*\)`).ReplaceAllString(result, `import('$1.js`+v+`')`)

	// Bare import "xxx.js" / import 'xxx.js' -> append version
	// Keep it conservative: only match import statements.
	result = regexp.MustCompile(`\bimport\s*"([^"]+)\.js"`).ReplaceAllString(result, `import "$1.js`+v+`"`)
	result = regexp.MustCompile(`\bimport\s*'([^']+)\.js'`).ReplaceAllString(result, `import '$1.js`+v+`'`)

	return result
}

// ShouldProcessJSForVersioning checks if a JS file from res.wx.qq.com should have its imports versioned
func (ji *JSInjector) ShouldProcessJSForVersioning(path string) bool {
	// Process all JS files that have our version parameter (indicating they came from versioned HTML)
	return strings.HasSuffix(strings.Split(path, "?")[0], ".js") &&
		strings.Contains(path, "v="+ji.version)
}

// IsTargetJSFile checks if the given path is a target JS file that needs injection
func (ji *JSInjector) IsTargetJSFile(path string) bool {
	// Target: virtual_svg-icons-register.publish*.js files from res.wx.qq.com
	return strings.Contains(path, "virtual_svg-icons-register") &&
		strings.HasSuffix(strings.Split(path, "?")[0], ".js")
}

// InjectAll applies all injection methods to the JS content
func (ji *JSInjector) InjectAll(jsContent string) string {
	result := ji.InjectMediaCapture(jsContent)
	result = ji.InjectCommentCapture(result)
	return result
}

// GetVersion returns the current version string
func (ji *JSInjector) GetVersion() string {
	return ji.version
}

// SetVersion sets a custom version string (useful for testing)
func (ji *JSInjector) SetVersion(version string) {
	ji.version = version
}

// HasMediaGetter checks if the JS content contains the media getter
func HasMediaGetter(jsContent string) bool {
	return mediaGetterRegex.MatchString(jsContent)
}

// HasCommentDetail checks if the JS content contains the finderGetCommentDetail method
func HasCommentDetail(jsContent string) bool {
	return commentDetailRegex.MatchString(jsContent)
}
