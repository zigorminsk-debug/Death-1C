/* Очистка сеансов 1С — клиентская часть интерфейса.
   Работает без внешних библиотек и без доступа в интернет. */
(function () {
  'use strict';

  var APP = window.APP || {};
  var TOKEN = APP.token || '';

  var state = APP.state || {};
  var cfg = APP.config || {};
  var secrets = APP.secrets || {};
  var contacts = APP.contacts || [];

  var plan = null;
  var sortKey = '';
  var sortAsc = false;
  var autoTimer = null;
  var busy = false;

  /* ------------------------------------------------------------------ */
  /* Вспомогательные функции                                             */
  /* ------------------------------------------------------------------ */

  function $(sel, root) { return (root || document).querySelector(sel); }
  function $$(sel, root) { return Array.prototype.slice.call((root || document).querySelectorAll(sel)); }

  function el(tag, attrs, text) {
    var node = document.createElement(tag);
    if (attrs) {
      Object.keys(attrs).forEach(function (k) {
        if (k === 'class') { node.className = attrs[k]; }
        else if (k === 'html') { node.innerHTML = attrs[k]; }
        else if (k.indexOf('on') === 0) { node.addEventListener(k.slice(2), attrs[k]); }
        else if (attrs[k] !== null && attrs[k] !== undefined) { node.setAttribute(k, attrs[k]); }
      });
    }
    if (text !== undefined && text !== null) { node.textContent = String(text); }
    return node;
  }

  function esc(s) {
    if (s === null || s === undefined) { return ''; }
    return String(s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function api(path, body) {
    var init = {
      method: body === undefined ? 'GET' : 'POST',
      headers: { 'X-Session-Token': TOKEN }
    };
    if (body !== undefined) {
      init.headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(body);
    }
    return fetch('/api/' + path, init).then(function (r) {
      return r.json().catch(function () { return {}; }).then(function (data) {
        if (r.status === 403) {
          throw new Error('Окно интерфейса потеряло связь с программой. Закройте окно и запустите программу заново.');
        }
        if (!r.ok && data && data.error) { throw new Error(data.error); }
        if (!r.ok) { throw new Error('Сервер интерфейса вернул код ' + r.status); }
        return data;
      });
    });
  }

  function pad(n) { return n < 10 ? '0' + n : String(n); }

  function fmtDateTime(iso) {
    if (!iso) { return '—'; }
    var d = new Date(iso);
    if (isNaN(d.getTime())) { return '—'; }
    return pad(d.getDate()) + '.' + pad(d.getMonth() + 1) + '.' + d.getFullYear() + ' ' +
      pad(d.getHours()) + ':' + pad(d.getMinutes()) + ':' + pad(d.getSeconds());
  }

  function fmtAgo(iso) {
    if (!iso) { return '—'; }
    var d = new Date(iso);
    if (isNaN(d.getTime())) { return '—'; }
    return fmtIdle(Math.floor((Date.now() - d.getTime()) / 60000));
  }

  function fmtIdle(min) {
    if (min === null || min === undefined || min < 0) { return '—'; }
    if (min === 0) { return 'только что'; }
    if (min < 60) { return min + ' мин'; }
    var h = Math.floor(min / 60);
    var m = min % 60;
    if (m === 0) { return h + ' ч'; }
    return h + ' ч ' + m + ' мин';
  }

  function fmtMem(mb) {
    if (!mb || mb <= 0) { return '—'; }
    if (mb < 1024) { return mb.toFixed(0) + ' МБ'; }
    return (mb / 1024).toFixed(1) + ' ГБ';
  }

  function busyOn(ids) {
    ids.forEach(function (id) { var n = $(id); if (n) { n.classList.add('busy'); } });
  }
  function busyOff(ids) {
    ids.forEach(function (id) { var n = $(id); if (n) { n.classList.remove('busy'); } });
  }

  function toast(title, text, kind) {
    var box = $('#toasts');
    if (!box) {
      box = el('div', { id: 'toasts' });
      document.body.appendChild(box);
    }
    var node = el('div', { class: 'toast ' + (kind || 'info') });
    if (title) { node.appendChild(el('b', null, title)); }
    if (text) { node.appendChild(el('span', null, text)); }
    box.appendChild(node);
    setTimeout(function () {
      node.style.opacity = '0';
      node.style.transition = 'opacity .25s';
      setTimeout(function () { if (node.parentNode) { node.parentNode.removeChild(node); } }, 260);
    }, 5200);
  }

  /* ------------------------------------------------------------------ */
  /* Модальные окна                                                      */
  /* ------------------------------------------------------------------ */

  function openModal(title, bodyNode, buttons) {
    $('#modal-title').textContent = title;
    var body = $('#modal-body');
    body.innerHTML = '';
    body.appendChild(bodyNode);
    var foot = $('#modal-foot');
    foot.innerHTML = '';
    (buttons || []).forEach(function (b) {
      var btn = el('button', { class: 'btn ' + (b.cls || ''), type: 'button', onclick: b.onClick }, b.label);
      foot.appendChild(btn);
    });
    $('#backdrop').hidden = false;
  }

  function closeModal() { $('#backdrop').hidden = true; }

  /* ------------------------------------------------------------------ */
  /* Шапка и сводка                                                      */
  /* ------------------------------------------------------------------ */

  function renderHeader() {
    $('#build-label').textContent = state.build_label || 'сборка';
    document.title = state.title || 'Очистка сеансов 1С';

    var pillEl = $('#pill-elevated');
    if (state.elevated) {
      pillEl.className = 'pill ok';
      pillEl.textContent = 'права администратора: есть';
      pillEl.title = 'Программа запущена с повышенными правами' +
        (state.current_user ? ' (' + state.current_user + ')' : '');
    } else {
      pillEl.className = 'pill bad';
      pillEl.textContent = 'нет прав администратора';
      pillEl.title = 'Запустите программу через ярлык — она запросит права администратора';
    }

    var pillC = $('#pill-connection');
    if (state.agent_version) {
      pillC.className = 'pill ok';
      pillC.textContent = '1С ' + state.address + ' · ' + state.agent_version;
    } else if (state.last_error) {
      pillC.className = 'pill bad';
      pillC.textContent = 'нет связи с ' + (state.address || 'сервером 1С');
    } else {
      pillC.className = 'pill';
      pillC.textContent = 'сервер 1С: ' + (state.address || 'не задан');
    }
    pillC.title = state.last_error || 'Сервер администрирования 1С (RAS), порт 1545';

    $('#pill-demo').hidden = !state.demo_mode;

    var banner = $('#alert-banner');
    if (!state.configured || state.first_run) {
      banner.hidden = false;
      banner.className = 'alert';
      banner.innerHTML = 'Программа запущена впервые: укажите адрес сервера 1С и учётные данные. ' +
        'Откройте <a href="#" id="banner-settings">настройки</a> (или запустите программу с удерживаемым Shift).';
      var link = $('#banner-settings');
      if (link) {
        link.addEventListener('click', function (e) { e.preventDefault(); showView('settings'); });
      }
    } else if (!state.elevated) {
      banner.hidden = false;
      banner.className = 'alert bad';
      banner.textContent = 'Программа работает без прав администратора. Закройте её и запустите через ярлык — ' +
        'ярлык всегда запрашивает права администратора.';
    } else if (state.last_error) {
      banner.hidden = false;
      banner.className = 'alert bad';
      banner.textContent = 'Ошибка подключения: ' + state.last_error;
    } else {
      banner.hidden = true;
    }
  }

  function renderCards() {
    var s = (plan && plan.summary) || state.summary || {};
    var cards = [
      { k: 'Всего сеансов', v: s.total || 0, s: 'на сервере 1С', cls: 'info' },
      { k: 'Пользователей', v: s.users || 0, s: (s.groups || 0) + ' групп', cls: '' },
      { k: 'Дубликатов', v: s.duplicates || 0, s: 'старых сеансов того же пользователя', cls: s.duplicates ? 'warn' : '' },
      { k: 'К завершению', v: s.to_terminate || 0, s: 'по текущему правилу', cls: s.to_terminate ? 'bad' : 'ok' },
      { k: 'Освободится', v: fmtMem(s.freed_memory_mb || 0), s: 'памяти рабочих процессов', cls: '' },
      { k: 'Служебных', v: s.service || 0, s: 'не завершаются никогда', cls: '' }
    ];
    var box = $('#cards');
    box.innerHTML = '';
    cards.forEach(function (c) {
      var node = el('div', { class: 'card ' + (c.cls || '') });
      node.appendChild(el('div', { class: 'k' }, c.k));
      node.appendChild(el('div', { class: 'v' }, c.v));
      node.appendChild(el('div', { class: 's' }, c.s));
      box.appendChild(node);
    });

    var cleanBtn = $('#btn-clean');
    cleanBtn.disabled = !(plan && plan.summary && plan.summary.to_terminate > 0);
    if (plan && plan.summary && plan.summary.to_terminate > 0) {
      cleanBtn.innerHTML = '<span class="ico">✕</span> Удалить старые (' + plan.summary.to_terminate + ')';
    } else {
      cleanBtn.innerHTML = '<span class="ico">✕</span> Удалить старые';
    }
  }

  /* ------------------------------------------------------------------ */
  /* Таблица сеансов                                                     */
  /* ------------------------------------------------------------------ */

  function visibleItems() {
    if (!plan || !plan.items) { return []; }
    var q = ($('#filter').value || '').trim().toLowerCase();
    var onlyProblem = $('#only-problem').checked;
    var hideService = $('#hide-service').checked;
    var showService = cfg.ui && cfg.ui.show_service_sessions;

    var items = plan.items.filter(function (it) {
      if (onlyProblem && it.action !== 'terminate') { return false; }
      if (hideService && it.is_service) { return false; }
      if (!hideService && showService === false && it.is_service) { return false; }
      if (!q) { return true; }
      var hay = [it.user_name, it.infobase_name, it.host, it.app_id, it.client_ip,
        it.cluster_name, it.uuid, it.licenses].join(' ').toLowerCase();
      return hay.indexOf(q) >= 0;
    });

    if (sortKey) {
      items.sort(function (a, b) {
        var va = a[sortKey], vb = b[sortKey];
        if (typeof va === 'string') { va = va.toLowerCase(); vb = String(vb || '').toLowerCase(); }
        if (va === vb) { return 0; }
        var r = va > vb ? 1 : -1;
        return sortAsc ? r : -r;
      });
    }
    return items;
  }

  function renderGrid() {
    var body = $('#grid-body');
    body.innerHTML = '';

    if (!plan || !plan.items || plan.items.length === 0) {
      var tr = el('tr', { class: 'empty-row' });
      var td = el('td', { colspan: '9' });
      var box = el('div', { class: 'empty' });
      box.appendChild(el('div', { class: 'empty-title' }, 'Сеансы ещё не загружены'));
      box.appendChild(el('div', { class: 'empty-text', html:
        'Нажмите «Показать сеансы», чтобы получить список с сервера 1С.<br>' +
        'Кнопка «Удалить старые» оставит по каждому пользователю только самые актуальные сеансы.' }));
      td.appendChild(box);
      tr.appendChild(td);
      body.appendChild(tr);
      return;
    }

    var items = visibleItems();
    if (items.length === 0) {
      var tr2 = el('tr', { class: 'empty-row' });
      var td2 = el('td', { colspan: '9' });
      var box2 = el('div', { class: 'empty' });
      box2.appendChild(el('div', { class: 'empty-title' }, 'Под фильтр ничего не подошло'));
      box2.appendChild(el('div', { class: 'empty-text' }, 'Измените условия отбора или сбросьте поиск.'));
      td2.appendChild(box2);
      tr2.appendChild(td2);
      body.appendChild(tr2);
      return;
    }

    var frag = document.createDocumentFragment();
    items.forEach(function (it) {
      var row = el('tr', { class: 'row-' + it.action, title: it.reason || '' });

      var tdUser = el('td');
      tdUser.appendChild(el('span', { class: 'cell-user' }, it.user_name || '(без имени)'));
      if (it.is_duplicate && it.group_size > 1) {
        tdUser.appendChild(el('span', { class: 'badge dupe', title: 'Сеансов в группе: ' + it.group_size },
          'дубль ' + it.rank + '/' + it.group_size));
      }
      if (it.hibernate) {
        tdUser.appendChild(el('span', { class: 'cell-sub' }, 'спящий сеанс'));
      }
      row.appendChild(tdUser);

      var tdIb = el('td');
      tdIb.appendChild(el('span', null, it.infobase_name || '—'));
      tdIb.appendChild(el('span', { class: 'cell-sub' }, it.cluster_name || ''));
      row.appendChild(tdIb);

      row.appendChild(el('td', { class: 'cell-dim' }, appLabel(it.app_id)));

      var tdHost = el('td');
      tdHost.appendChild(el('span', null, it.host || '—'));
      if (it.client_ip) { tdHost.appendChild(el('span', { class: 'cell-sub' }, it.client_ip)); }
      row.appendChild(tdHost);

      row.appendChild(el('td', { class: 'num cell-dim' }, fmtDateTime(it.started_at)));
      row.appendChild(el('td', { class: 'num cell-dim' }, fmtDateTime(it.last_active_at)));

      var idle = el('td', { class: 'num' }, fmtIdle(it.idle_min));
      if (it.idle_min > 120 && it.action === 'terminate') { idle.style.color = '#ff9d9d'; }
      row.appendChild(idle);

      row.appendChild(el('td', { class: 'num cell-dim' }, fmtMem(it.memory_mb)));

      var tdAct = el('td');
      var cls = it.action === 'terminate' ? 'terminate' : (it.action === 'keep' ? 'keep' : 'skip');
      var label = it.action === 'terminate' ? 'завершить' : (it.action === 'keep' ? 'оставить' : 'не трогать');
      tdAct.appendChild(el('span', { class: 'badge ' + cls }, label));
      if (it.reason) { tdAct.appendChild(el('span', { class: 'reason' }, it.reason)); }
      row.appendChild(tdAct);

      row.addEventListener('click', function () { showSessionDetails(it); });
      frag.appendChild(row);
    });
    body.appendChild(frag);
  }

  function appLabel(id) {
    var map = {
      '1CV8C': 'тонкий клиент',
      '1CV8': 'толстый клиент',
      'BackgroundJob': 'регламентное задание',
      'SrvrConsole': 'консоль сервера',
      'COMConnection': 'COM-соединение',
      'Designer': 'Конфигуратор',
      'WebServerExtension': 'веб-сервер',
      'JobScheduler': 'планировщик заданий'
    };
    if (!id) { return '—'; }
    return map[id] ? map[id] + ' (' + id + ')' : id;
  }

  function showSessionDetails(it) {
    var body = el('div');
    var dl = el('dl', { class: 'kv' });
    var rows = [
      ['Пользователь', it.user_name || '(без имени)'],
      ['Информационная база', (it.infobase_name || '—') + '  ·  ' + (it.infobase_id || '—')],
      ['Кластер', (it.cluster_name || '—') + '  ·  ' + (it.cluster_id || '—')],
      ['Приложение', appLabel(it.app_id)],
      ['Компьютер', it.host || '—'],
      ['IP-адрес клиента', it.client_ip || '—'],
      ['Номер сеанса', String(it.id || '—')],
      ['Идентификатор сеанса', it.uuid || '—'],
      ['Рабочий процесс', it.process_id || '—'],
      ['Запущен', fmtDateTime(it.started_at) + ' (возраст ' + fmtIdle(it.age_min) + ')'],
      ['Последняя активность', fmtDateTime(it.last_active_at) + ' (простой ' + fmtIdle(it.idle_min) + ')'],
      ['Спящий сеанс', it.hibernate ? 'да' : 'нет'],
      ['Память', fmtMem(it.memory_mb)],
      ['Вызовов всего / за 5 мин', (it.calls_all || 0) + ' / ' + (it.calls_last5min || 0)],
      ['Локаль', it.locale || '—'],
      ['Разделение данных', it.data_separation || '—'],
      ['Текущая служба', it.current_service || '—'],
      ['Лицензии', it.licenses || '—'],
      ['Группа', (it.group_title || '—') + ' (сеансов в группе: ' + (it.group_size || 0) + ', позиция ' + (it.rank || 0) + ')'],
      ['Решение программы', (it.action === 'terminate' ? 'завершить' : (it.action === 'keep' ? 'оставить' : 'не трогать'))],
      ['Пояснение', it.reason || '—']
    ];
    rows.forEach(function (r) {
      if (!r || r.length < 2 || r[2] === null) { return; }
      dl.appendChild(el('dt', null, r[0]));
      dl.appendChild(el('dd', null, r[1]));
    });
    body.appendChild(dl);
    openModal('Сеанс · ' + (it.user_name || '(без имени)'), body, [
      { label: 'Закрыть', cls: 'ghost', onClick: closeModal }
    ]);
  }

  /* ------------------------------------------------------------------ */
  /* Журнал                                                              */
  /* ------------------------------------------------------------------ */

  function renderLog(entries) {
    var box = $('#log-body');
    if (!entries || entries.length === 0) {
      box.textContent = 'журнал пуст';
      return;
    }
    box.innerHTML = '';
    entries.forEach(function (e) {
      var cls = e.level === 'error' ? 'log-error' : (e.level === 'warn' ? 'log-warn' :
        (e.level === 'action' ? 'log-action' : ''));
      var line = el('div', { class: cls }, e.time + '  ' + e.message);
      box.appendChild(line);
    });
    box.scrollTop = box.scrollHeight;
  }

  function loadLog() {
    api('log').then(function (d) { renderLog(d.entries); }).catch(function () { /* журнал не критичен */ });
  }

  /* ------------------------------------------------------------------ */
  /* Основные действия                                                   */
  /* ------------------------------------------------------------------ */

  function applyState(newState) {
    if (newState) { state = newState; }
    renderHeader();
    renderCards();
  }

  function doRefresh(silent) {
    if (busy) { return Promise.resolve(); }
    busy = true;
    busyOn(['#btn-show', '#btn-refresh', '#btn-clean']);
    if (!silent) { $('#btn-show').innerHTML = '<span class="spinner"></span> Загрузка…'; }
    return api('refresh', {}).then(function (d) {
      plan = d.plan;
      applyState(d.state);
      renderGrid();
      loadLog();
      if (!silent) {
        toast('Сеансы получены', 'Всего ' + plan.summary.total + ', к завершению ' + plan.summary.to_terminate, 'ok');
      }
      return d;
    }).catch(function (err) {
      toast('Не удалось получить сеансы', err.message, 'bad');
      $('#alert-banner').hidden = false;
      $('#alert-banner').className = 'alert bad';
      $('#alert-banner').textContent = 'Ошибка подключения: ' + err.message;
      throw err;
    }).finally(function () {
      busy = false;
      busyOff(['#btn-show', '#btn-refresh', '#btn-clean']);
      $('#btn-show').innerHTML = '<span class="ico">▤</span> Показать сеансы';
      renderCards();
    });
  }

  function doClean() {
    doRefresh(true).then(function () {
      if (!plan || !plan.summary || plan.summary.to_terminate === 0) {
        toast('Устаревших сеансов нет', 'По каждому пользователю остались только актуальные сеансы.', 'ok');
        return;
      }
      if (cfg.policy && cfg.policy.confirm === false) {
        executePlan(planTargets().map(function (i) { return i.uuid; }));
        return;
      }
      showConfirm(planTargets());
    }).catch(function () { /* ошибка уже показана */ });
  }

  function planTargets() {
    if (!plan || !plan.items) { return []; }
    return plan.items.filter(function (i) { return i.action === 'terminate'; });
  }

  function showConfirm(targets) {
    var body = el('div');
    body.appendChild(el('p', { class: 'plan-summary', html:
      'Будет принудительно завершено <b>' + targets.length + '</b> устаревших сеансов. ' +
      'Актуальные сеансы пользователей останутся работать.<br>' +
      'Пользователь увидит сообщение: <i>' + esc((cfg.policy || {}).terminate_message || '') + '</i>' }));

    var list = el('div', { class: 'plan-list' });
    targets.forEach(function (t) {
      var item = el('div', { class: 'plan-item' });
      item.appendChild(el('b', null, (t.user_name || '(без имени)') + ' · ' + (t.infobase_name || '—')));
      item.appendChild(el('span', { class: 'why' },
        t.host + ' · простой ' + fmtIdle(t.idle_min) + ' · запущен ' + fmtDateTime(t.started_at)));
      list.appendChild(item);
    });
    body.appendChild(list);

    openModal('Подтверждение: завершить ' + targets.length + ' сеансов', body, [
      { label: 'Отмена', cls: 'ghost', onClick: closeModal },
      {
        label: 'Завершить сеансы', cls: 'danger', onClick: function () {
          closeModal();
          executePlan(targets.map(function (i) { return i.uuid; }));
        }
      }
    ]);
  }

  function executePlan(uuids) {
    busy = true;
    busyOn(['#btn-show', '#btn-refresh', '#btn-clean']);
    api('execute', { uuids: uuids, force: false }).then(function (d) {
      plan = d.plan;
      applyState(d.state);
      renderGrid();
      loadLog();
      showResult(d.result);
    }).catch(function (err) {
      toast('Ошибка при завершении сеансов', err.message, 'bad');
    }).finally(function () {
      busy = false;
      busyOff(['#btn-show', '#btn-refresh', '#btn-clean']);
      renderCards();
    });
  }

  function showResult(res) {
    var body = el('div');
    body.appendChild(el('p', { class: 'plan-summary' }, res.message || ''));
    var list = el('div', { class: 'result-list' });
    (res.items || []).forEach(function (i) {
      var cls = i.status === 'terminated' ? '' : (i.status === 'failed' ? 'failed' : 'skipped');
      var item = el('div', { class: 'result-item ' + cls });
      var label = i.status === 'terminated' ? 'завершён' : (i.status === 'failed' ? 'ошибка' : 'пропущен');
      item.appendChild(el('b', null, (i.user || '(без имени)') + ' · ' + (i.infobase || '—') + ' · ' + label));
      item.appendChild(el('span', { class: 'detail' }, i.detail || ''));
      list.appendChild(item);
    });
    body.appendChild(list);
    openModal('Результат очистки', body, [
      { label: 'Закрыть', cls: 'ghost', onClick: closeModal },
      { label: 'Показать сеансы заново', cls: 'primary', onClick: function () { closeModal(); doRefresh(true); } }
    ]);
    if (res.failed > 0) {
      toast('Есть ошибки', res.failed + ' сеансов не удалось завершить', 'bad');
    } else if (res.terminated > 0) {
      toast('Готово', 'Завершено сеансов: ' + res.terminated, 'ok');
    }
  }

  /* ------------------------------------------------------------------ */
  /* Настройки                                                           */
  /* ------------------------------------------------------------------ */

  function renderSettings() {
    var o = cfg.onec || {}, w = cfg.windows || {}, p = cfg.policy || {}, u = cfg.ui || {};

    $('#onec-host').value = o.host || '';
    $('#onec-port').value = o.port || 1545;
    $('#onec-user').value = '';
    $('#onec-user').placeholder = secrets.onec_user_masked ? 'сохранено: ' + secrets.onec_user_masked + ' (введите, чтобы заменить)' : 'администратор кластера 1С';
    $('#onec-password').value = '';
    $('#onec-password').placeholder = secrets.onec_password_set ? 'пароль сохранён (введите, чтобы заменить)' : 'пароль администратора кластера';
    $('#onec-timeout').value = o.timeout_sec || 30;
    $('#onec-demo').checked = !!o.demo_mode;

    $('#win-host').value = w.host || '';
    $('#win-user').value = '';
    $('#win-user').placeholder = secrets.windows_user_masked ? 'сохранено: ' + secrets.windows_user_masked + ' (введите, чтобы заменить)' : 'DOMAIN\\Administrator';
    $('#win-password').value = '';
    $('#win-password').placeholder = secrets.windows_password_set ? 'пароль сохранён (введите, чтобы заменить)' : 'пароль администратора Windows';
    $('#win-manage').checked = w.manage_services !== false;

    $('#pol-group').value = p.group_by || 'user_ib';
    $('#pol-keep').value = p.keep_per_group || 1;
    $('#pol-sort').value = p.sort_by || 'activity';
    $('#pol-idle').value = p.min_idle_min || 0;
    $('#pol-age').value = p.max_age_hours || 0;
    $('#pol-apps').value = (p.exclude_app_ids || []).join(', ');
    $('#pol-users').value = (p.exclude_users || []).join(', ');
    $('#pol-skip-empty').checked = p.skip_empty_user !== false;
    $('#pol-confirm').checked = p.confirm !== false;
    $('#pol-message').value = p.terminate_message || '';

    $('#ui-window').value = u.window_mode || 'app';
    $('#ui-autorefresh').value = u.auto_refresh_sec || 0;
    $('#ui-show-service').checked = u.show_service_sessions !== false;
    $('#hide-service').checked = u.show_service_sessions === false;
    $('#auto-refresh').checked = (u.auto_refresh_sec || 0) > 0;

    renderSecretState();
    renderStorageInfo();
    setupAutoRefresh();
  }

  function renderSecretState() {
    $('#onec-secret-state').innerHTML =
      '<b>Администратор кластера:</b> ' + (secrets.onec_user_masked ? esc(secrets.onec_user_masked) : 'не задан') +
      ' &nbsp;·&nbsp; <b>пароль:</b> ' + (secrets.onec_password_set ? 'сохранён' : 'не задан') +
      '<br>Показать сохранённые значения нельзя — они не покидают зашифрованное хранилище.';
    $('#win-secret-state').innerHTML =
      '<b>Администратор Windows:</b> ' + (secrets.windows_user_masked ? esc(secrets.windows_user_masked) : 'не задан') +
      ' &nbsp;·&nbsp; <b>пароль:</b> ' + (secrets.windows_password_set ? 'сохранён' : 'не задан') +
      '<br>Используются только для проверки и запуска служб 1С на сервере.';
  }

  function renderStorageInfo() {
    $('#storage-info').innerHTML =
      '<b>Где лежат настройки</b><br><code>' + esc(secrets.store_path || state.data_dir || '') + '</code><br>' +
      '<b>Защита:</b> ' + esc(secrets.protection || '') + '<br>' +
      '<b>Файл настроек:</b> ' + (secrets.store_exists ? 'создан' : 'ещё не создан') + '<br>' +
      '<b>Каталог данных:</b> <code>' + esc(state.data_dir || '') + '</code>';
  }

  function splitList(v) {
    return String(v || '').split(',').map(function (s) { return s.trim(); }).filter(function (s) { return s !== ''; });
  }

  function collectConfig() {
    return {
      onec: {
        host: $('#onec-host').value.trim(),
        port: parseInt($('#onec-port').value, 10) || 1545,
        user: $('#onec-user').value,
        password: $('#onec-password').value,
        timeout_sec: parseInt($('#onec-timeout').value, 10) || 30,
        demo_mode: $('#onec-demo').checked
      },
      windows: {
        host: $('#win-host').value.trim(),
        user: $('#win-user').value,
        password: $('#win-password').value,
        manage_services: $('#win-manage').checked
      },
      policy: {
        group_by: $('#pol-group').value,
        keep_per_group: parseInt($('#pol-keep').value, 10) || 1,
        min_idle_min: parseInt($('#pol-idle').value, 10) || 0,
        max_age_hours: parseInt($('#pol-age').value, 10) || 0,
        exclude_app_ids: splitList($('#pol-apps').value),
        exclude_users: splitList($('#pol-users').value),
        skip_empty_user: $('#pol-skip-empty').checked,
        terminate_message: $('#pol-message').value,
        confirm: $('#pol-confirm').checked,
        sort_by: $('#pol-sort').value
      },
      ui: {
        auto_refresh_sec: parseInt($('#ui-autorefresh').value, 10) || 0,
        window_mode: $('#ui-window').value,
        show_service_sessions: $('#ui-show-service').checked
      }
    };
  }

  function saveSettings() {
    var payload = {
      config: collectConfig(),
      clear_onec_password: window.__clearOneC === true,
      clear_windows_password: window.__clearWin === true
    };
    window.__clearOneC = false;
    window.__clearWin = false;
    var st = $('#save-status');
    st.className = 'save-status';
    st.textContent = 'сохранение…';
    api('config', payload).then(function (d) {
      cfg = d.config; secrets = d.secrets;
      if (d.state) { state = d.state; }
      renderSettings();
      renderHeader();
      st.className = 'save-status ok';
      st.textContent = d.message || 'сохранено';
      toast('Настройки сохранены', 'Учётные данные зашифрованы и больше не отображаются', 'ok');
      loadLog();
      setTimeout(function () { st.textContent = ''; }, 5000);
    }).catch(function (err) {
      st.className = 'save-status bad';
      st.textContent = err.message;
      toast('Не удалось сохранить', err.message, 'bad');
    });
  }

  function testConnection(kind) {
    var btn = kind === 'onec' ? $('#btn-test-onec') : $('#btn-test-win');
    var old = btn.innerHTML;
    btn.innerHTML = '<span class="spinner"></span> проверка…';
    btn.disabled = true;
    api('test/' + kind, {}).then(function (r) {
      showTestResult(kind === 'onec' ? 'Проверка подключения к серверу 1С' : 'Проверка учётных данных Windows', r);
    }).catch(function (err) {
      showTestResult('Проверка подключения', { ok: false, message: err.message, details: [] });
    }).finally(function () {
      btn.innerHTML = old;
      btn.disabled = false;
    });
  }

  function showTestResult(title, r) {
    var body = el('div');
    body.appendChild(el('p', { class: 'plan-summary' }, r.message || ''));
    if (r.details && r.details.length) {
      var ul = el('ul', { class: 'plan-list' });
      r.details.forEach(function (d) {
        ul.appendChild(el('li', { class: 'plan-item ' + (r.ok ? 'keep' : '') }, d));
      });
      body.appendChild(ul);
    }
    openModal(title, body, [{ label: 'Закрыть', cls: 'ghost', onClick: closeModal }]);
    if (r.ok) { toast('Проверка пройдена', r.message, 'ok'); }
    else { toast('Проверка не пройдена', r.message, 'bad'); }
  }

  function loadServices() {
    var box = $('#services-list');
    box.innerHTML = '<span class="hint"><span class="spinner"></span> запрос к серверу…</span>';
    api('services').then(function (d) {
      box.innerHTML = '';
      if (!d.services || d.services.length === 0) {
        box.appendChild(el('span', { class: 'hint' }, 'Службы 1С на сервере не найдены.'));
        return;
      }
      d.services.forEach(function (s) {
        var row = el('div', { class: 'service-row' });
        var name = el('div', { class: 'name' });
        name.appendChild(el('b', null, s.display || s.name));
        name.appendChild(el('span', null, s.name + ' · ' + (s.kind || '') ));
        row.appendChild(name);
        row.appendChild(el('span', { class: 'badge ' + (s.running ? 'keep' : 'skip') },
          s.running ? 'работает' : 'остановлена'));
        var start = el('button', { class: 'btn tiny', type: 'button' }, 'Запустить');
        start.addEventListener('click', function () { serviceAction(s.name, 'start', start); });
        var restart = el('button', { class: 'btn tiny', type: 'button' }, 'Перезапустить');
        restart.addEventListener('click', function () { serviceAction(s.name, 'restart', restart); });
        row.appendChild(start);
        if (s.running) { row.appendChild(restart); }
        box.appendChild(row);
      });
    }).catch(function (err) {
      box.innerHTML = '';
      box.appendChild(el('span', { class: 'hint' }, err.message));
      toast('Службы недоступны', err.message, 'bad');
    });
  }

  function serviceAction(name, action, btn) {
    var old = btn.textContent;
    btn.disabled = true;
    btn.textContent = '…';
    api('service-action', { name: name, action: action }).then(function (r) {
      toast('Служба ' + name, r.message || 'команда выполнена', r.ok === false ? 'bad' : 'ok');
      loadServices();
      loadLog();
    }).catch(function (err) {
      toast('Не удалось управлять службой', err.message, 'bad');
    }).finally(function () {
      btn.disabled = false;
      btn.textContent = old;
    });
  }

  function wipeCredentials() {
    var body = el('div');
    body.appendChild(el('p', { class: 'plan-summary', html:
      'Из зашифрованного хранилища будут удалены логин и пароль администратора кластера 1С ' +
      'и логин и пароль администратора Windows-сервера.<br>Адреса серверов и правила очистки останутся.' }));
    openModal('Забыть учётные данные', body, [
      { label: 'Отмена', cls: 'ghost', onClick: closeModal },
      {
        label: 'Удалить учётные данные', cls: 'danger', onClick: function () {
          api('wipe', {}).then(function (d) {
            cfg = d.config; secrets = d.secrets;
            renderSettings();
            closeModal();
            toast('Учётные данные удалены', 'При следующем подключении их нужно ввести заново', 'ok');
          }).catch(function (err) { toast('Ошибка', err.message, 'bad'); });
        }
      }
    ]);
  }

  /* ------------------------------------------------------------------ */
  /* О программе                                                         */
  /* ------------------------------------------------------------------ */

  function openAbout() {
    var body = el('div');
    body.appendChild(el('div', { class: 'dev-name' }, state.developer || 'Zakharevich Igor'));
    body.appendChild(el('p', { class: 'hint' }, 'Разработчик программы «' + (state.product || 'Очистка сеансов 1С') + '»'));

    var cs = el('div', { class: 'contacts' });
    (contacts.length ? contacts : [
      { kind: 'name', label: 'Разработчик', value: state.developer, link: '' },
      { kind: 'email', label: 'Электронная почта', value: state.email, link: state.email_link },
      { kind: 'phone', label: 'Телефон', value: state.phone, link: state.phone_link }
    ]).forEach(function (c) {
      if (!c.value) { return; }
      var row = el('div', { class: 'contact' });
      row.appendChild(el('span', { class: 'label' }, c.label));
      if (c.link) {
        var a = el('a', { href: c.link }, c.value);
        a.addEventListener('click', function (e) {
          // Ссылка mailto:/tel: откроется системным обработчиком;
          // запасной вызов гарантирует, что контакт кликабельен и в окне приложения.
          api('open', { url: c.link }).catch(function () { /* не критично */ });
        });
        row.appendChild(a);
      } else {
        row.appendChild(el('span', null, c.value));
      }
      cs.appendChild(row);
    });
    body.appendChild(cs);

    var dl = el('dl', { class: 'kv' });
    [
      ['Продукт', (state.product || '') + ' ' + (state.version || '')],
      ['Порядковый номер сборки', state.build_number ? '№' + state.build_number : 'не присвоен'],
      ['Канал сборки', state.channel || '—'],
      ['Коммит', state.commit || '—'],
      ['Дата сборки', state.build_date ? fmtDateTime(state.build_date) : '—'],
      ['Компьютер', (state.hostname || '—') + ' · ' + (state.current_user || '—')],
      ['Права администратора', state.elevated ? 'есть' : 'нет'],
      ['Время работы', state.uptime || '—'],
      ['Каталог данных', state.data_dir || '—'],
      ['Файл настроек', secrets.store_path || '—'],
      ['Защита учётных данных', secrets.protection || '—']
    ].forEach(function (r) {
      dl.appendChild(el('dt', null, r[0]));
      dl.appendChild(el('dd', null, r[1]));
    });
    body.appendChild(dl);
    body.appendChild(el('p', { class: 'hint', html: esc(state.one_line || '') }));

    openModal('О программе', body, [
      { label: 'Написать разработчику', cls: 'ghost', onClick: function () { openLink(state.email_link); } },
      { label: 'Позвонить', cls: 'ghost', onClick: function () { openLink(state.phone_link); } },
      { label: 'Закрыть', cls: 'primary', onClick: closeModal }
    ]);
  }

  function openLink(url) {
    if (!url) { return; }
    api('open', { url: url }).catch(function () { window.location.href = url; });
  }

  /* ------------------------------------------------------------------ */
  /* Переключение экранов                                                */
  /* ------------------------------------------------------------------ */

  function showView(name) {
    var settings = name === 'settings';
    $('#view-main').hidden = settings;
    $('#view-settings').hidden = !settings;
    $('#btn-settings').textContent = settings ? 'К сеансам' : 'Настройки';
    if (settings) { renderSettings(); }
  }

  function setupAutoRefresh() {
    if (autoTimer) { clearInterval(autoTimer); autoTimer = null; }
    var sec = parseInt($('#ui-autorefresh').value, 10) || 0;
    if (!sec && $('#auto-refresh').checked) { sec = 60; }
    if (sec > 0) {
      autoTimer = setInterval(function () {
        if (!busy && !$('#view-main').hidden) { doRefresh(true); }
      }, sec * 1000);
    }
  }

  /* ------------------------------------------------------------------ */
  /* Инициализация                                                       */
  /* ------------------------------------------------------------------ */

  function bind() {
    $('#btn-show').addEventListener('click', function () { doRefresh(false); });
    $('#btn-refresh').addEventListener('click', function () { doRefresh(true); });
    $('#btn-clean').addEventListener('click', doClean);

    $('#btn-settings').addEventListener('click', function () {
      showView($('#view-settings').hidden ? 'settings' : 'main');
    });
    $('#btn-back').addEventListener('click', function () { showView('main'); });
    $('#btn-about').addEventListener('click', openAbout);
    $('#btn-quit').addEventListener('click', function () {
      var body = el('p', { class: 'plan-summary' }, 'Программа будет закрыта. Сохранённые настройки останутся на сервере.');
      openModal('Выход', body, [
        { label: 'Отмена', cls: 'ghost', onClick: closeModal },
        {
          label: 'Выйти', cls: 'danger', onClick: function () {
            api('quit', {}).catch(function () { /* окно закроется вместе с сервером */ });
            document.body.innerHTML = '<div class="empty"><div class="empty-title">Программа закрыта</div>' +
              '<div class="empty-text">Окно можно закрыть.</div></div>';
          }
        }
      ]);
    });

    $('#filter').addEventListener('input', renderGrid);
    $('#only-problem').addEventListener('change', renderGrid);
    $('#hide-service').addEventListener('change', renderGrid);
    $('#auto-refresh').addEventListener('change', function () {
      if (this.checked && (parseInt($('#ui-autorefresh').value, 10) || 0) === 0) {
        $('#ui-autorefresh').value = 60;
      }
      if (!this.checked) { $('#ui-autorefresh').value = 0; }
      setupAutoRefresh();
    });

    $$('.grid thead th.sortable').forEach(function (th) {
      th.addEventListener('click', function () {
        var key = th.getAttribute('data-sort');
        if (sortKey === key) { sortAsc = !sortAsc; } else { sortKey = key; sortAsc = true; }
        $$('.grid thead th.sortable').forEach(function (o) {
          o.textContent = o.textContent.replace(/ [▲▼]$/, '');
        });
        th.textContent = th.textContent + (sortAsc ? ' ▲' : ' ▼');
        renderGrid();
      });
    });

    $('#btn-save').addEventListener('click', saveSettings);
    $('#btn-test-onec').addEventListener('click', function () { testConnection('onec'); });
    $('#btn-test-win').addEventListener('click', function () { testConnection('windows'); });
    $('#btn-services').addEventListener('click', loadServices);
    $('#btn-wipe').addEventListener('click', wipeCredentials);
    $('#btn-clear-onec').addEventListener('click', function () {
      window.__clearOneC = true;
      $('#onec-password').value = '';
      $('#onec-user').value = '';
      toast('Пароль будет очищен', 'Нажмите «Сохранить настройки», чтобы применить', 'warn');
    });
    $('#btn-clear-win').addEventListener('click', function () {
      window.__clearWin = true;
      $('#win-password').value = '';
      $('#win-user').value = '';
      toast('Пароль будет очищен', 'Нажмите «Сохранить настройки», чтобы применить', 'warn');
    });

    $('#btn-log-refresh').addEventListener('click', loadLog);
    $('#btn-log-save').addEventListener('click', function () {
      var url = '/api/log.txt';
      fetch(url, { headers: { 'X-Session-Token': TOKEN } })
        .then(function (r) { return r.text(); })
        .then(function (text) {
          var blob = new Blob([text], { type: 'text/plain;charset=utf-8' });
          var a = document.createElement('a');
          a.href = URL.createObjectURL(blob);
          a.download = 'session-cleaner-log.txt';
          document.body.appendChild(a);
          a.click();
          document.body.removeChild(a);
        })
        .catch(function (err) { toast('Не удалось сохранить журнал', err.message, 'bad'); });
    });

    $('#modal-close').addEventListener('click', closeModal);
    $('#backdrop').addEventListener('click', function (e) { if (e.target === this) { closeModal(); } });
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape') { closeModal(); }
      if (e.key === 'F5') { e.preventDefault(); doRefresh(true); }
    });

    $('#onec-demo').addEventListener('change', function () {
      ['#onec-host', '#onec-port', '#onec-user', '#onec-password'].forEach(function (id) {
        $(id).disabled = $('#onec-demo').checked;
      });
    });
  }

  function heartbeat() {
    setInterval(function () {
      api('heartbeat', {}).catch(function () { /* сервер мог остановиться */ });
    }, 20000);
  }

  function init() {
    renderHeader();
    renderCards();
    bind();
    loadLog();
    showView(APP.startView === 'settings' ? 'settings' : 'main');
    heartbeat();
    if (APP.firstRun || APP.startView === 'settings') {
      toast('Требуется настройка', 'Укажите адрес сервера 1С и учётные данные — они сохраняются один раз', 'warn');
    }
    if (state.demo_mode) {
      toast('Демонстрационный режим', 'Показаны учебные сеансы встроенного сервера 1С', 'info');
    }
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
