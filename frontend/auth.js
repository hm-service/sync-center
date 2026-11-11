I18N.apply();

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
    note.textContent = I18N.t("ui.login-offline");
    return;
  }

  if (response.status === 401) {
    note.textContent = I18N.t("ui.login-invalid");
    return;
  }
  if (!response.ok) {
    note.textContent = I18N.t("ui.login-failed", { status: response.status });
    return;
  }
  location.href = "/ui";
});
