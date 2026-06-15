// api.ts — typed client for the demo BACKEND REST API only.
// The frontend NEVER calls the OVDB server directly; everything goes through
// the Go backend at :5180, which holds the scoped vault token.

const BACKEND = "http://localhost:5180";

export interface Task {
  id: string;
  title: string;
  done: boolean;
  createdAt: string;
}

async function json<T>(res: Response): Promise<T> {
  if (!res.ok) {
    let msg = res.statusText;
    try {
      const body = await res.json();
      if (body?.error) msg = body.error;
    } catch {
      /* keep statusText */
    }
    throw new Error(msg);
  }
  // 204 No Content has no body.
  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}

export const api = {
  // The connect flow is a full-page navigation: the backend 302s to OVDB and,
  // after consent, 302s back to this frontend.
  connectUrl: `${BACKEND}/connect`,

  async status(): Promise<boolean> {
    const res = await fetch(`${BACKEND}/api/status`);
    const body = await json<{ connected: boolean }>(res);
    return body.connected;
  },

  async list(): Promise<Task[]> {
    return json<Task[]>(await fetch(`${BACKEND}/api/tasks`));
  },

  async create(title: string): Promise<Task> {
    return json<Task>(
      await fetch(`${BACKEND}/api/tasks`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ title }),
      }),
    );
  },

  async update(id: string, patch: { done?: boolean; title?: string }): Promise<Task> {
    return json<Task>(
      await fetch(`${BACKEND}/api/tasks/${encodeURIComponent(id)}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(patch),
      }),
    );
  },

  async remove(id: string): Promise<void> {
    await json<void>(
      await fetch(`${BACKEND}/api/tasks/${encodeURIComponent(id)}`, {
        method: "DELETE",
      }),
    );
  },
};
