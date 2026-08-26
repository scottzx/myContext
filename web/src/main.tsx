import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import { ErrorBoundary } from "./components/ErrorBoundary";
import { HttpDataSource, tokenFromUrl } from "./datasource";
import "./style.css";

const token = tokenFromUrl();
const ds = new HttpDataSource(token);

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ErrorBoundary>
      <App ds={ds} />
    </ErrorBoundary>
  </StrictMode>,
);
