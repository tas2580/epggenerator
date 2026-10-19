'use strict';

const $ = (id) => document.getElementById(id);
let state = null;
let settingsDirty = false;
let lastSignature = '';
let dialogChannel = null;
let editingChannel = null;
let selectedEPG = '';
let requestPending = false;
let polling = false;
let connectionError = '';
let actionError = '';
const number = (n) => new Intl.NumberFormat('de-DE').format(n);
const date = (value) => value && !value.startsWith('0001-') ? new Date(value).toLocaleString('de-DE', {dateStyle:'short', timeStyle:'short'}) : null;
const epgName = (id) => state?.epgChannels.find((c) => c.id === id)?.names?.[0] || id;

function node(tag, className, text) {
  const element = document.createElement(tag);
  if (className) element.className = className;
  if (text !== undefined) element.textContent = text;
  return element;
}

async function api(path, method = 'GET', body) {
  const response = await fetch(path, {method, cache:'no-store', headers:{'Content-Type':'application/json'}, body:body === undefined ? undefined : JSON.stringify(body)});
  const data = await response.json();
  if (!response.ok) throw new Error(data.error || `HTTP ${response.status}`);
  return data;
}

function showStatus() {
  const busy = requestPending || state?.busy;
  document.querySelectorAll('[data-mutation]').forEach((button) => { button.disabled = !!busy; });
  $('activity').classList.toggle('busy', !!busy);
  $('activity').textContent = busy ? 'Quellen und Ausgabe werden verarbeitet. Die bisherige Ausgabe bleibt verfügbar.' : state?.hasOutput ? 'Bereit · Senderänderungen und Zuordnungen werden direkt in die Ausgabe übernommen.' : 'Noch keine Ausgabe. Bitte Quellen einstellen und aktualisieren.';
  const error = actionError || connectionError || state?.lastError;
  $('notice').hidden = !error;
  $('notice').textContent = error ? `${error}${state?.hasOutput ? ' Die letzte gültige Ausgabe bleibt verfügbar.' : ''}` : '';
}

function updatePlaylistSource() {
  const local = $('playlist-source-type').value === 'file';
  for (const id of ['playlist-file', 'playlist-file-label', 'playlist-file-hint']) $(id).hidden = !local;
  for (const id of ['playlist-url', 'playlist-url-label']) $(id).hidden = local;
  $('playlist-file').disabled = !local;
  $('playlist-file').required = local;
  $('playlist-url').disabled = local;
  $('playlist-url').required = !local;
}

function formSettings() {
  return {
    epgUrl: $('epg-url').value,
    playlistUrl: $('playlist-url').value,
    playlistFile: $('playlist-source-type').value === 'file' ? $('playlist-file').value : '',
  };
}

function renderState() {
  if (!settingsDirty) {
    $('epg-url').value = state.settings.epgUrl;
    $('playlist-url').value = state.settings.playlistUrl;
    $('playlist-file').value = state.settings.playlistFile || '';
    $('playlist-source-type').value = state.settings.playlistFile ? 'file' : 'url';
    updatePlaylistSource();
  }
  $('total').textContent = number(state.channels.length);
  $('mapped').textContent = number(state.mapped);
  $('unmapped').textContent = `${number(state.channels.length - state.mapped)} ohne Programmdaten-Zuordnung`;
  $('epg-count').textContent = number(state.epgChannels.length);
  $('programmes').textContent = number(state.outputProgrammes);
  $('generated').textContent = date(state.lastGenerated) ? `Erstellt: ${date(state.lastGenerated)}` : 'Noch keine Ausgabe';
  $('last-refresh').textContent = date(state.lastRefresh) ? `Quellen aktualisiert: ${date(state.lastRefresh)}` : 'Noch nicht aktualisiert';
  $('interval-note').textContent = `Automatische Aktualisierung alle ${number(state.intervalSeconds / 3600)} Stunden.`;
  for (const id of ['epg-link', 'playlist-link', 'epg-download', 'playlist-download']) {
    $(id).classList.toggle('disabled-link', !state.hasOutput);
    $(id).setAttribute('aria-disabled', String(!state.hasOutput));
    $(id).tabIndex = state.hasOutput ? 0 : -1;
  }
  $('epg-link').textContent = new URL('/epg.xml', location.href).href;
  $('playlist-link').textContent = new URL('/playlist.m3u8', location.href).href;
  const exact = exactMatches();
  $('accept-exact').hidden = exact.length === 0;
  $('accept-exact').textContent = `${exact.length} ID-Treffer übernehmen`;
  const signature = JSON.stringify([state.channels, state.epgChannels]);
  if (signature !== lastSignature) {
    lastSignature = signature;
    const group = $('group-filter').value;
    $('group-filter').replaceChildren(new Option('Alle Kategorien', ''));
    [...new Set(state.channels.map((c) => c.group).filter(Boolean))].sort((a,b) => a.localeCompare(b,'de')).forEach((g) => $('group-filter').add(new Option(g,g)));
    $('group-filter').value = [...$('group-filter').options].some((o) => o.value === group) ? group : '';
    renderRows();
    if ($('mapping-dialog').open) {
      dialogChannel = state.channels.find((c) => c.key === dialogChannel?.key);
      if (!dialogChannel) $('mapping-dialog').close();
      else renderOptions();
    }
    if ($('channel-dialog').open && !state.channels.some((c) => c.key === editingChannel?.key)) $('channel-dialog').close();
  }
  showStatus();
}

function exactMatches() {
  return state?.channels.filter((c) => c.status === 'unmapped' && c.suggestions.some((s) => s.id === c.sourceId)) || [];
}

function renderRows() {
  if (!state) return;
  const query = $('search').value.toLocaleLowerCase('de').trim();
  const filter = $('status-filter').value;
  const group = $('group-filter').value;
  const rows = document.createDocumentFragment();
  let visible = 0;
  state.channels.forEach((channel, index) => {
    if (query && !`${channel.name} ${channel.group} ${channel.originalName} ${channel.sourceId} ${channel.id}`.toLocaleLowerCase('de').includes(query)) return;
    if (group && group !== channel.group) return;
    if (filter === 'review' && !['missing','ambiguous'].includes(channel.status)) return;
    if (['mapped','unmapped'].includes(filter) && channel.status !== filter) return;
    visible++;
    const row = node('tr');
    row.append(node('td','',String(index + 1).padStart(2,'0')));
    const source = node('td');
    const summary = node('div','channel-summary');
    const info = node('div','channel-info');
    info.append(node('span','channel-name',channel.name));
    info.append(node('span','channel-detail',channel.group || 'Ohne Kategorie'));
    if (!channel.sourceId || channel.sourceId !== channel.id) info.append(node('span','channel-detail','Stabile ID ergänzt'));
    if (channel.edited) info.append(node('span','edited-label','Angepasst'));
    summary.append(logoPreview(channel),info);
    source.append(summary);
    row.append(source);
    const mapping = node('td');
    if (channel.status === 'mapped') {
      mapping.append(node('span','mapping-name',epgName(channel.mapping)), node('span','channel-detail',channel.mapping));
    } else if (channel.status === 'missing') {
      mapping.append(node('span','mapping-name','EPG-Sender nicht mehr verfügbar'),node('span','channel-detail',channel.mapping));
    } else if (channel.status === 'ambiguous') {
      mapping.append(node('span','mapping-name','Sender nicht eindeutig erkennbar'),node('span','channel-detail','Bitte eindeutige tvg-id in der Quelle ergänzen.'));
    } else if (channel.suggestions.length) {
      const suggestion = channel.suggestions[0];
      const label = node('div','suggestion','Vorschlag: ');
      label.append(node('strong','',epgName(suggestion.id)));
      mapping.append(label);
      const accept = node('button','text-button',`${suggestion.reason} · Übernehmen`);
      accept.type = 'button';
      accept.dataset.mutation = '';
      accept.setAttribute('aria-label', `Vorschlag ${epgName(suggestion.id)} für ${channel.name} übernehmen`);
      accept.addEventListener('click', () => saveMappings({[channel.key]:suggestion.id}));
      mapping.append(accept);
    } else mapping.append(node('span','channel-detail','Noch keine Programmdaten zugeordnet'));
    row.append(mapping);
    const statusCell = node('td');
    statusCell.append(node('span',`badge ${channel.status}`,{mapped:'Zugeordnet',unmapped:'Ohne EPG',missing:'Zu prüfen',ambiguous:'Zu prüfen'}[channel.status]));
    row.append(statusCell);
    const actions = node('td');
    const actionButtons = node('div','channel-actions');
    const edit = node('button','secondary','Bearbeiten');
    edit.type = 'button';
    edit.dataset.mutation = '';
    edit.setAttribute('aria-label', `Name und Kategorie von ${channel.name} bearbeiten`);
    edit.addEventListener('click', () => openChannelEdit(channel));
    const button = node('button','secondary',channel.mapping ? 'EPG ändern' : 'EPG zuordnen');
    button.type = 'button';
    button.dataset.mutation = '';
    button.setAttribute('aria-label', `EPG-Zuordnung für ${channel.name} ändern`);
    button.addEventListener('click', () => openMapping(channel));
    if (channel.ambiguous) {
      button.textContent = 'ID prüfen';
      button.disabled = true;
      delete button.dataset.mutation;
      edit.disabled = true;
      edit.title = 'Bitte zuerst eine eindeutige Sender-ID in der Quelle ergänzen.';
      delete edit.dataset.mutation;
    }
    actionButtons.append(edit,button);
    actions.append(actionButtons);
    row.append(actions);
    rows.append(row);
  });
  $('channel-rows').replaceChildren(rows);
  $('visible-count').textContent = `${visible} von ${state.channels.length}`;
  $('empty').hidden = visible > 0;
  $('empty').textContent = state.channels.length ? 'Keine Sender für diesen Filter gefunden.' : 'Noch keine Sender geladen. Stelle oben deine Quellen ein und starte die Aktualisierung.';
  showStatus();
}

function logoPreview(channel) {
  const frame = node('div','channel-logo');
  const fallback = node('span','logo-fallback',channel.name.slice(0,2).toLocaleUpperCase('de'));
  fallback.setAttribute('aria-hidden','true');
  frame.append(fallback);
  frame.title = 'Kein Senderlogo vorhanden';
  if (channel.logo) {
    const image = node('img');
    image.alt = `Logo von ${channel.name}`;
    image.loading = 'lazy';
    image.decoding = 'async';
    image.classList.add('pending');
    image.addEventListener('load', () => { image.classList.remove('pending'); image.hidden = false; fallback.hidden = true; frame.title = channel.name; });
    image.addEventListener('error', () => { image.hidden = true; fallback.hidden = false; frame.title = 'Logo nicht erreichbar'; });
    frame.title = 'Logo wird geladen';
    image.src = `/api/logo?key=${encodeURIComponent(channel.key)}&v=${encodeURIComponent(channel.logo + '|' + state.lastRefresh)}`;
    frame.append(image);
  }
  return frame;
}

function openChannelEdit(channel) {
  editingChannel = channel;
  $('channel-name').value = channel.name;
  $('channel-group').value = channel.group;
  $('channel-edit-error').hidden = true;
  $('channel-original').textContent = `Quelle: ${channel.originalName} · ${channel.originalGroup || 'Ohne Kategorie'}`;
  $('edit-channel-logo').replaceChildren(logoPreview(channel));
  const categories = [...new Set(state.channels.map((c) => c.group).filter(Boolean))].sort((a,b) => a.localeCompare(b,'de'));
  $('channel-categories').replaceChildren(...categories.map((category) => new Option(category,category)));
  $('channel-dialog').showModal();
  $('channel-name').focus();
}

async function saveChannelEdit(change) {
  if (!editingChannel || requestPending || state?.busy) return;
  const saved = await mutate('/api/channels','PATCH',{channels:{[editingChannel.key]:change}});
  if (saved) $('channel-dialog').close();
  else {
    $('channel-edit-error').textContent = actionError || 'Speichern fehlgeschlagen. Bitte erneut versuchen.';
    $('channel-edit-error').hidden = false;
  }
}

function openMapping(channel) {
  dialogChannel = channel;
  selectedEPG = channel.status === 'mapped' ? channel.mapping : '';
  $('dialog-title').textContent = channel.name;
  $('epg-search').value = '';
  renderOptions();
  $('mapping-dialog').showModal();
  $('epg-search').focus();
}

function renderOptions() {
  const query = $('epg-search').value.trim().toLocaleLowerCase('de');
  const fragment = document.createDocumentFragment();
  const suggested = new Map(dialogChannel.suggestions.map((s,i) => [s.id,i]));
  const options = [{id:'', names:['Keine Zuordnung']}].concat([...state.epgChannels].sort((a,b) => (suggested.get(a.id) ?? 999) - (suggested.get(b.id) ?? 999) || epgName(a.id).localeCompare(epgName(b.id),'de')));
  let count = 0;
  for (const channel of options) {
    if (channel.id && query && !`${channel.id} ${channel.names.join(' ')}`.toLocaleLowerCase('de').includes(query)) continue;
    count++;
    const button = node('button',`epg-option${channel.id === selectedEPG ? ' selected' : ''}`);
    button.type = 'button';
    button.setAttribute('aria-pressed', String(channel.id === selectedEPG));
    const label = node('span','',channel.names[0] || channel.id);
    label.append(node('small','',channel.id || 'Sender bleibt erhalten, ohne Programmdaten'));
    button.append(label);
    if (suggested.has(channel.id)) button.append(node('span','option-note','Vorschlag'));
    button.addEventListener('click', () => {
      selectedEPG = channel.id;
      $('epg-options').querySelectorAll('button').forEach((b) => {b.classList.remove('selected');b.setAttribute('aria-pressed','false');});
      button.classList.add('selected');button.setAttribute('aria-pressed','true');
      $('selection-label').textContent = selectedEPG ? epgName(selectedEPG) : 'Keine Zuordnung · Sender bleibt erhalten';
    });
    fragment.append(button);
  }
  if (count === 1 && query) fragment.append(node('p','empty','Keine passenden EPG-Sender gefunden.'));
  $('epg-options').replaceChildren(fragment);
  $('selection-label').textContent = selectedEPG ? epgName(selectedEPG) : 'Keine Zuordnung · Sender bleibt erhalten';
}

async function loadState() {
  if (polling) return;
  polling = true;
  try {
    const first = !state;
    state = await api('/api/state');
    connectionError = '';
    if (first && !state.hasOutput) $('sources').open = true;
    renderState();
  } catch (error) {
    connectionError = `Verbindung zum Dienst fehlgeschlagen: ${error.message}`;
    showStatus();
  } finally { polling = false; }
}

async function mutate(path, method, body) {
  if (requestPending || state?.busy) return false;
  requestPending = true;actionError = '';showStatus();
  try {
    await api(path,method,body);
    return true;
  } catch (error) {
    actionError = error.message;
    return false;
  } finally {
    requestPending = false;
    await loadState();
    showStatus();
  }
}

async function saveMappings(mappings) { return mutate('/api/mappings','PATCH',{mappings}); }
$('settings-form').addEventListener('input', () => { settingsDirty = true; });
$('playlist-source-type').addEventListener('change', () => { settingsDirty = true; updatePlaylistSource(); });
$('settings-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  const settings = formSettings();
  if (await mutate('/api/settings','PUT',settings)) {
    if (JSON.stringify(formSettings()) === JSON.stringify(settings)) settingsDirty = false;
    renderState();
  }
});
$('refresh').addEventListener('click', () => mutate('/api/refresh','POST',{}));
$('search').addEventListener('input',renderRows);
$('status-filter').addEventListener('change',renderRows);
$('group-filter').addEventListener('change',renderRows);
$('epg-search').addEventListener('input',renderOptions);
$('save-mapping').addEventListener('click',async () => {
  if (dialogChannel && await saveMappings({[dialogChannel.key]:selectedEPG})) $('mapping-dialog').close();
});
$('accept-exact').addEventListener('click', () => saveMappings(Object.fromEntries(exactMatches().map((c) => [c.key,c.sourceId]))));
$('close-channel-dialog').addEventListener('click', () => $('channel-dialog').close());
$('channel-edit-form').addEventListener('submit', (event) => {
  event.preventDefault();
  saveChannelEdit({name:$('channel-name').value,group:$('channel-group').value});
});
$('reset-channel').addEventListener('click', () => saveChannelEdit(null));
loadState();
setInterval(loadState,2500);
