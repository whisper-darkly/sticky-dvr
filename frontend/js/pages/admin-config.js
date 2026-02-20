// admin-config.js — Global config editor (admin only)

import * as api from '../api.js';
import { escape } from '../utils.js';

export function cleanup() {}

export async function render(container) {
  container.innerHTML = `
    <div class="page-header">
      <div>
        <div class="page-title">System Configuration</div>
        <div class="page-subtitle">Global backend settings</div>
      </div>
    </div>
    <div id="config-content"><div class="loading-wrap"><span class="spinner"></span> Loading…</div></div>`;

  async function load() {
    const el = document.getElementById('config-content');
    try {
      const cfg = await api.getConfig();
      renderForm(cfg);
    } catch (err) {
      el.innerHTML = `<div class="alert alert-error">Failed to load config: ${escape(err.message)}</div>`;
    }
  }

  function renderForm(cfg) {
    const el = document.getElementById('config-content');
    const fields = Object.entries(cfg);

    el.innerHTML = `
      <div class="card">
        <div class="card-title">Configuration</div>
        <div id="config-alert"></div>
        <form id="config-form">
          ${fields.length ? fields.map(([k, v]) => renderField(k, v)).join('') : `
            <p style="color:var(--text-muted);font-size:.9rem">No configuration keys set. Use the JSON editor below to add values.</p>
          `}
          <div class="form-group">
            <label class="form-label">Raw JSON</label>
            <textarea class="form-control" id="cfg-json" rows="8" style="font-family:monospace;font-size:12px">${escape(JSON.stringify(cfg, null, 2))}</textarea>
            <div class="form-hint">Edit raw JSON — this is the authoritative value that will be saved.</div>
          </div>
          <div style="display:flex;gap:.6rem;margin-top:1rem">
            <button class="btn btn-primary" type="submit" id="cfg-save">Save</button>
            <button class="btn btn-ghost" type="button" id="cfg-reload">Reload</button>
          </div>
        </form>
      </div>`;

    const form = el.querySelector('#config-form');
    const alertEl = el.querySelector('#config-alert');
    const jsonEl = el.querySelector('#cfg-json');

    // Keep simple fields in sync with JSON textarea
    form.querySelectorAll('.cfg-field').forEach(input => {
      input.addEventListener('input', () => syncToJson(form, jsonEl));
    });

    el.querySelector('#cfg-reload').onclick = load;

    form.addEventListener('submit', async (e) => {
      e.preventDefault();
      alertEl.innerHTML = '';
      let data;
      try {
        data = JSON.parse(jsonEl.value);
      } catch {
        alertEl.innerHTML = '<div class="alert alert-error">Invalid JSON.</div>';
        return;
      }
      const btn = el.querySelector('#cfg-save');
      btn.disabled = true;
      try {
        await api.putConfig(data);
        alertEl.innerHTML = '<div class="alert alert-success">Configuration saved.</div>';
        await load();
      } catch (err) {
        alertEl.innerHTML = `<div class="alert alert-error">Save failed: ${escape(err.message)}</div>`;
      } finally {
        btn.disabled = false;
      }
    });
  }

  function renderField(key, value) {
    const isNum  = typeof value === 'number';
    const isBool = typeof value === 'boolean';
    if (isBool) {
      return `
        <div class="form-group">
          <label class="form-label">${escape(key)}</label>
          <select class="form-control cfg-field" data-key="${escape(key)}">
            <option value="true"  ${value ? 'selected' : ''}>true</option>
            <option value="false" ${!value ? 'selected' : ''}>false</option>
          </select>
        </div>`;
    }
    if (isNum || typeof value === 'string') {
      return `
        <div class="form-group">
          <label class="form-label">${escape(key)}</label>
          <input class="form-control cfg-field" type="${isNum ? 'number' : 'text'}"
            data-key="${escape(key)}" data-type="${isNum ? 'number' : 'string'}"
            value="${escape(String(value))}" />
        </div>`;
    }
    return ''; // complex values handled via JSON editor only
  }

  function syncToJson(form, jsonEl) {
    try {
      const obj = JSON.parse(jsonEl.value);
      form.querySelectorAll('.cfg-field').forEach(input => {
        const key = input.dataset.key;
        const type = input.dataset.type;
        if (type === 'number') obj[key] = parseFloat(input.value) || 0;
        else if (input.tagName === 'SELECT') obj[key] = input.value === 'true';
        else obj[key] = input.value;
      });
      jsonEl.value = JSON.stringify(obj, null, 2);
    } catch {}
  }

  await load();
}
