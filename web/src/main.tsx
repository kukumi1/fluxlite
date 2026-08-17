import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import { applyStoredTheme } from "./theme";
import "./styles.css";

applyStoredTheme();

const container = document.getElementById("root");
if (!container) {
  throw new Error("root element missing");
}

createRoot(container).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
