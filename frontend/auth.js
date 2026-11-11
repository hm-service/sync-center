const form = document.getElementById("login");
const input = document.getElementById("token");
const note = document.getElementById("note");

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  note.textContent = "";

  let response;
  try {
    response = await fetch("/api/auth", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ token: input.value }),
    });
  } catch (error) {
    note.textContent = "无法连接服务";
    return;
  }

  if (response.status === 401) {
    note.textContent = "口令不正确";
    return;
  }
  if (!response.ok) {
    note.textContent = "登录失败：" + response.status;
    return;
  }
  location.href = "/ui";
});
