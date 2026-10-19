(function () {
  "use strict";

  var RETRY_MS = 3000;
  var STORAGE_KEY = "kidsroom.camera";

  var loader = document.getElementById("loader");
  var loaderNote = document.getElementById("loaderNote");
  var video = document.getElementById("video");
  var spinner = document.getElementById("spinner");
  var errorBox = document.getElementById("error");
  var dropdown = document.getElementById("camDropdown");
  var toggle = document.getElementById("camDropdownToggle");
  var menu = document.getElementById("camDropdownMenu");

  var cameras = [];
  var currentId = null;
  var hls = null;
  var retryTimer = null;

  // ---------- экран загрузки ----------

  function hideLoader() {
    loader.classList.add("hidden");
  }

  function loaderMessage(text) {
    loaderNote.textContent = text;
    loaderNote.hidden = false;
  }

  function setSpinner(on) {
    spinner.hidden = !on;
  }

  function showError(text) {
    errorBox.textContent = text;
    errorBox.hidden = false;
  }

  function clearError() {
    errorBox.hidden = true;
  }

  function scheduleRetry(reason) {
    if (retryTimer) return;
    showError(reason + " Переподключение…");
    setSpinner(true);
    retryTimer = setTimeout(function () {
      retryTimer = null;
      play(currentId);
    }, RETRY_MS);
  }

  // ---------- список камер ----------

  function renderMenu() {
    menu.innerHTML = "";
    cameras.forEach(function (cam) {
      var item = document.createElement("a");
      item.className = "cam-dropdown-item" + (cam.id === currentId ? " active" : "");
      item.textContent = cam.name;
      item.href = "#" + cam.id;
      item.addEventListener("click", function (e) {
        e.preventDefault();
        closeMenu();
        if (cam.id !== currentId) {
          try { localStorage.setItem(STORAGE_KEY, cam.id); } catch (err) { /* приватный режим */ }
          play(cam.id);
        }
      });
      var li = document.createElement("li");
      li.appendChild(item);
      menu.appendChild(li);
    });
    // С одной камерой переключать нечего.
    dropdown.hidden = cameras.length < 2;
  }

  function openMenu() {
    dropdown.classList.add("open");
    toggle.setAttribute("aria-expanded", "true");
  }

  function closeMenu() {
    dropdown.classList.remove("open");
    toggle.setAttribute("aria-expanded", "false");
  }

  toggle.addEventListener("click", function (e) {
    e.stopPropagation();
    if (dropdown.classList.contains("open")) closeMenu(); else openMenu();
  });

  document.addEventListener("click", closeMenu);
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape") closeMenu();
  });

  // ---------- проигрывание ----------

  function stopPlayback() {
    if (hls) {
      hls.destroy();
      hls = null;
    }
    video.removeAttribute("src");
    video.load();
  }

  function play(id) {
    if (!id) return;
    currentId = id;
    renderMenu();
    clearError();
    setSpinner(true);
    stopPlayback();

    var url = "hls/" + encodeURIComponent(id) + "/index.m3u8";

    if (window.Hls && window.Hls.isSupported()) {
      hls = new window.Hls({
        lowLatencyMode: true,
        liveSyncDurationCount: 3,
        manifestLoadingMaxRetry: 4,
        levelLoadingMaxRetry: 4,
        fragLoadingMaxRetry: 6
      });
      hls.on(window.Hls.Events.MANIFEST_PARSED, function () {
        clearError();
        video.play().catch(function () {
          // Автозапуск со звуком браузер может запретить — показываем контролы.
          setSpinner(false);
          hideLoader();
        });
      });
      hls.on(window.Hls.Events.ERROR, function (_evt, data) {
        if (!data || !data.fatal) return;
        if (data.type === window.Hls.ErrorTypes.NETWORK_ERROR) {
          scheduleRetry("Нет связи с потоком.");
        } else if (data.type === window.Hls.ErrorTypes.MEDIA_ERROR) {
          // Сначала мягкое восстановление, без полного пересоздания.
          try { hls.recoverMediaError(); } catch (err) { scheduleRetry("Ошибка видео."); }
        } else {
          scheduleRetry("Поток недоступен.");
        }
      });
      hls.loadSource(url);
      hls.attachMedia(video);
    } else if (video.canPlayType("application/vnd.apple.mpegurl")) {
      // Safari и iOS играют HLS сами.
      video.src = url;
      video.play().catch(function () { setSpinner(false); hideLoader(); });
    } else {
      setSpinner(false);
      hideLoader();
      showError("Браузер не умеет воспроизводить HLS. Откройте страницу в Chrome, Edge или Safari.");
    }
  }

  video.addEventListener("playing", function () {
    hideLoader();
    setSpinner(false);
    clearError();
  });
  video.addEventListener("waiting", function () { setSpinner(true); });
  video.addEventListener("stalled", function () { setSpinner(true); });
  video.addEventListener("error", function () { scheduleRetry("Поток прервался."); });

  // ---------- старт ----------

  function start() {
    fetch("cameras.json", { cache: "no-store" })
      .then(function (r) {
        if (!r.ok) throw new Error("HTTP " + r.status);
        return r.json();
      })
      .then(function (list) {
        cameras = Array.isArray(list) ? list : [];
        if (cameras.length === 0) {
          loaderMessage("Камеры ещё не настроены. Добавьте камеру в настройках агента: значок в трее → «Настройки».");
          return;
        }
        var saved = null;
        try { saved = localStorage.getItem(STORAGE_KEY); } catch (err) { /* приватный режим */ }
        var exists = cameras.some(function (c) { return c.id === saved; });
        play(exists ? saved : cameras[0].id);
      })
      .catch(function (err) {
        loaderMessage("Агент не отвечает (" + err.message + "). Повторная попытка через 5 секунд.");
        setTimeout(start, 5000);
      });
  }

  start();
})();
