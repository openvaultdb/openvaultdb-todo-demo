// main.ts — minimal to-do UI. When not connected it shows a Connect button;
// once a vault is connected it shows the add/list/toggle/delete to-do list.
import "./style.css";
import { api, type Task } from "./api";

const app = document.querySelector<HTMLDivElement>("#app")!;

let connected = false;
let tasks: Task[] = [];
let error: string | null = null;
let focusAdd = false; // refocus the add input after a task is added

async function refresh(): Promise<void> {
  try {
    connected = await api.status();
    tasks = connected ? await api.list() : [];
    // Show newest first. createdAt is server-set RFC3339, so a string compare
    // is chronological; descending puts the latest on top.
    tasks.sort((a, b) => b.createdAt.localeCompare(a.createdAt));
    error = null;
  } catch (e) {
    error = e instanceof Error ? e.message : String(e);
  }
  render();
}

async function run(fn: () => Promise<unknown>): Promise<void> {
  try {
    await fn();
    error = null;
  } catch (e) {
    error = e instanceof Error ? e.message : String(e);
  }
  await refresh();
}

function render(): void {
  app.innerHTML = "";

  const card = el("div", "card");
  card.append(el("h1", "title", "OpenVaultDB To-Do"));
  card.append(el("p", "subtitle", "A demo 3rd-party app backed by an OpenVaultDB vault."));

  if (error) card.append(el("div", "error", error));

  if (!connected) {
    const btn = el("button", "connect", "Connect your vault") as HTMLButtonElement;
    btn.onclick = () => {
      // Full-page navigation into the backend connect flow.
      window.location.href = api.connectUrl;
    };
    card.append(btn);
    card.append(el("p", "hint", "Connect to OpenVaultDB to start adding tasks."));
    app.append(card);
    return;
  }

  card.append(el("span", "badge", "Vault connected"));

  // Add form.
  const form = document.createElement("form");
  form.className = "add";
  const input = document.createElement("input");
  input.placeholder = "What needs doing?";
  input.autofocus = true;
  const add = el("button", "", "Add") as HTMLButtonElement;
  add.type = "submit";
  form.append(input, add);
  form.onsubmit = (e) => {
    e.preventDefault();
    const title = input.value.trim();
    if (!title) return;
    input.value = "";
    focusAdd = true; // return focus to the input after the re-render
    void run(() => api.create(title));
  };
  card.append(form);

  // List.
  const ul = document.createElement("ul");
  ul.className = "list";
  if (tasks.length === 0) {
    ul.append(el("li", "empty", "No tasks yet."));
  }
  for (const t of tasks) {
    const li = document.createElement("li");
    li.className = t.done ? "done" : "";

    const cb = document.createElement("input");
    cb.type = "checkbox";
    cb.checked = t.done;
    cb.onchange = () => void run(() => api.update(t.id, { done: cb.checked }));

    const label = el("span", "label", t.title);

    const del = el("button", "del", "✕") as HTMLButtonElement;
    del.title = "Delete";
    del.onclick = () => void run(() => api.remove(t.id));

    li.append(cb, label, del);
    ul.append(li);
  }
  card.append(ul);
  app.append(card);

  if (focusAdd) {
    focusAdd = false;
    input.focus();
  }
}

function el(tag: string, className: string, text?: string): HTMLElement {
  const e = document.createElement(tag);
  if (className) e.className = className;
  if (text !== undefined) e.textContent = text;
  return e;
}

void refresh();
