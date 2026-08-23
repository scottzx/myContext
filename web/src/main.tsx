import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import { HttpDataSource, tokenFromUrl } from "./datasource";
import "./style.css";

const token = tokenFromUrl();
const ds = new HttpDataSource(token);

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App ds={ds} />
  </StrictMode>,
);
