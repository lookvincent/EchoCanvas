import type { Metadata } from "next";
import "./globals.css";
import { TopNav } from "@/components/TopNav";

export const metadata: Metadata = {
  title: "EchoCanvas Studio — Multi-Modal Visual Narrative Engine",
  description:
    "Consistent multi-frame visual storyboard generation with HITL Gates, Layered Memory, VLM QA, and Transparent Cost Ledger.",
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="en">
      <body className="min-h-screen bg-[var(--bg-canvas)] text-[var(--text-primary)]">
        <TopNav />
        <main className="mx-auto max-w-[1600px] px-4 py-6 sm:px-6">{children}</main>
      </body>
    </html>
  );
}
