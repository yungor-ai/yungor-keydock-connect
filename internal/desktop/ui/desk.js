"use strict";
const $ = (id) => document.getElementById(id);
const token =
  location.hash.slice(1) || sessionStorage.getItem("keydock-session") || "";
if (location.hash) {
  sessionStorage.setItem("keydock-session", token);
  history.replaceState(null, "", location.pathname);
}
let state = {},
  busy = false,
  done = false;
function notice(text, error = false) {
  $("notice").textContent = text;
  $("notice").classList.toggle("error", error);
}
function render(updateModel = false) {
  const active = state.proxy,
    imported = state.imported;
  $("deviceBadge").textContent = imported ? "已导入设备" : "尚未导入设备";
  $("deviceBadge").classList.toggle("on", !!imported);
  $("codexState").textContent = state.configured ? "已配置" : "尚未配置";
  $("codexState").classList.toggle("on", !!state.configured);
  $("proxyState").textContent = active ? "已启用" : "已关闭";
  $("proxyState").classList.toggle("on", !!active);
  $("device").textContent = state.device || "尚未导入";
  $("gateway").textContent = state.base || "—";
  $("toggle").textContent = active ? "关闭系统代理" : "启用系统代理";
  if (updateModel) $("model").value = state.model || "";
  for (const id of ["model", "loadModels", "applyBtn", "applyOnly"])
    $(id).disabled = done || busy || !imported;
  $("toggle").disabled = done || busy || !(active || state.subscription);
  $("restore").disabled =
    done || busy || !(state.restorable || state.configured);
  $("launch").hidden = !state.configured;
  for (const id of ["importBtn", "quit", "launch"])
    $(id).disabled = busy || done;
}
async function post(route, body) {
  const h = { "X-KeyDock-Session": token };
  if (body && !(body instanceof FormData))
    h["Content-Type"] = "application/json";
  const res = await fetch("/api/" + route, {
    method: "POST",
    headers: h,
    body,
    cache: "no-store",
  });
  const v = await res.json();
  if (!res.ok) throw new Error(v.error || "操作失败");
  return v;
}
async function run(fn) {
  if (busy || done) return;
  busy = true;
  render();
  notice("处理中…");
  try {
    await fn();
  } catch (err) {
    notice(
      err.message === "Failed to fetch"
        ? "无法连接接入助手，请重新打开程序。"
        : err.message,
      true,
    );
  } finally {
    busy = false;
    render();
  }
}
function ask(title, text) {
  $("dialogTitle").textContent = title;
  $("dialogMessage").textContent = text;
  const d = $("confirm");
  d.returnValue = "";
  d.showModal();
  return new Promise((resolve) =>
    d.addEventListener("close", () => resolve(d.returnValue === "yes"), {
      once: true,
    }),
  );
}
$("accept").onclick = () => $("confirm").close("yes");
$("cancel").onclick = () => $("confirm").close();
$("importForm").onsubmit = (e) => {
  e.preventDefault();
  run(async () => {
    const f = new FormData();
    f.append("password", $("password").value);
    f.append("package", $("pkg").files[0]);
    try {
      state = await post("import", f);
    } finally {
      $("password").value = "";
    }
    $("models").replaceChildren();
    $("pkg").value = "";
    render(true);
    notice("设备包已导入。核对默认模型后，即可应用 Codex 配置。");
  });
};
$("loadModels").onclick = () =>
  run(async () => {
    const v = await post("models");
    const items = v.models || [];
    $("models").replaceChildren(
      ...items.map((id) => {
        const o = document.createElement("option");
        o.value = id;
        return o;
      }),
    );
    if (items.length === 1 && !$("model").value) $("model").value = items[0];
    $("model").focus();
    notice(
      items.length
        ? "已读取 " + items.length + " 个模型，可在默认模型栏选择。"
        : "网关未返回可选模型，可手动输入模型名称。",
    );
  });
$("configForm").onsubmit = (e) => {
  e.preventDefault();
  const open = e.submitter?.id !== "applyOnly";
  run(async () => {
    state = await post("apply", JSON.stringify({ model: $("model").value }));
    render(true);
    if (open) {
      try {
        await post("launch");
      } catch (err) {
        notice("配置已保存。" + err.message, true);
        return;
      }
    }
    notice(
      open
        ? "已应用配置并请求打开 Codex。如果 Codex 已在运行，请完全退出后重新打开。"
        : "配置已应用。请完全退出已运行的 Codex，再打开新会话。",
    );
  });
};
$("launch").onclick = () =>
  run(async () => {
    state = await post("launch");
    notice("已请求打开 Codex。");
  });
$("restore").onclick = async () => {
  if (await ask("恢复原 Codex 配置？", "将撤销接入助手对 Codex 的配置。"))
    run(async () => {
      state = await post("restore");
      notice("已恢复原 Codex 配置。");
    });
};
$("toggle").onclick = () =>
  run(async () => {
    state = await post(state.proxy ? "proxy/off" : "proxy/on");
    notice(state.proxy ? "系统代理已启用。" : "系统代理已关闭。");
  });
$("quit").onclick = async () => {
  if (await ask("退出接入助手？", "退出时将关闭本程序启用的系统代理。"))
    run(async () => {
      await post("quit");
      done = true;
      sessionStorage.removeItem("keydock-session");
      notice("接入助手已退出，可以关闭此窗口。");
    });
};
run(async () => {
  state = await post("state");
  render(true);
  notice("");
});

// A running core may stop while the window is backgrounded. Reconcile status
// without replacing a model name the user is editing or interrupting an action.
window.addEventListener("focus", async () => {
  if (busy || done) return;
  try {
    const latest = await post("state");
    if (!busy && !done) {
      state = latest;
      render();
    }
  } catch {
    /* A closed desktop host is reported by the next explicit action. */
  }
});
